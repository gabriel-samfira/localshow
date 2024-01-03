package database

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/gabriel-samfira/localshow/config"
	"github.com/gabriel-samfira/localshow/params"
	"github.com/pkg/errors"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newDBConn(dbCfg config.Database) (conn *gorm.DB, err error) {
	connURI, err := dbCfg.GormParams()
	if err != nil {
		return nil, errors.Wrap(err, "getting DB URI string")
	}

	gormConfig := &gorm.Config{}
	if !dbCfg.Debug {
		gormConfig.Logger = logger.Default.LogMode(logger.Silent)
	}

	conn, err = gorm.Open(sqlite.Open(connURI), gormConfig)
	if err != nil {
		return nil, errors.Wrap(err, "connecting to database")
	}

	if dbCfg.Debug {
		conn = conn.Debug()
	}
	return conn, nil
}

func NewSQLDatabase(ctx context.Context, cfg config.Database) (*SQLDatabase, error) {
	conn, err := newDBConn(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "creating DB connection")
	}

	var geoIPConn *geoIP
	if cfg.GeoIPDBFile != "" {
		geoIPConn, err = newGeoIP(cfg.GeoIPDBFile)
		if err != nil {
			return nil, errors.Wrap(err, "creating geoip connection")
		}
	}
	db := &SQLDatabase{
		conn:  conn,
		ctx:   ctx,
		cfg:   cfg,
		geoIP: geoIPConn,
	}

	if err := db.migrateDB(); err != nil {
		return nil, errors.Wrap(err, "migrating database")
	}
	return db, nil
}

type SQLDatabase struct {
	conn  *gorm.DB
	ctx   context.Context
	cfg   config.Database
	geoIP *geoIP
}

func (s *SQLDatabase) migrateDB() error {
	if !s.conn.Migrator().HasTable(&RemoteAddress{}) && s.conn.Migrator().HasTable(&AuthAttempt{}) {
		if err := s.conn.AutoMigrate(&RemoteAddress{}); err != nil {
			return errors.Wrap(err, "running auto migrate")
		}

		if err := s.SyncRemoteAddressesFromAuthAttempts(); err != nil {
			return errors.Wrap(err, "syncing remote addresses from auth attempts")
		}
	}
	if err := s.conn.AutoMigrate(
		&AuthAttempt{},
		&RemoteAddress{},
	); err != nil {
		return errors.Wrap(err, "running auto migrate")
	}

	return nil
}

func (s *SQLDatabase) SyncRemoteAddressesFromAuthAttempts() error {
	err := s.conn.Transaction(func(tx *gorm.DB) error {
		var totalRows int64
		if err := tx.Model(&AuthAttempt{}).Count(&totalRows).Error; err != nil {
			return err
		}

		max := 1000
		for i := 0; i < int(totalRows)/max+1; i++ {
			var authAttempts []AuthAttempt
			if err := tx.Limit(max).Offset(i * max).Find(&authAttempts).Error; err != nil {
				return err
			}

			for _, authAttempt := range authAttempts {
				if err := s.upsertRemoteAddress(tx, authAttempt.RemoteAddress); err != nil {
					return err
				}
			}
		}
		return nil
	})

	return err
}

func (s *SQLDatabase) upsertRemoteAddress(tx *gorm.DB, remoteAddress string) error {
	var country string
	var city string
	locationRecord, err := s.geoIP.GetRecord(remoteAddress)
	if err == nil {
		city = locationRecord.City.Names["en"]
		country = locationRecord.Country.Names["en"]
	}

	var remoteAddressEntry RemoteAddress
	if err := tx.Where("address = ?", remoteAddress).First(&remoteAddressEntry).Error; err != nil {
		if err := tx.Create(&RemoteAddress{
			Address:  remoteAddress,
			Attempts: 1,
			City:     city,
			Country:  country,
		}).Error; err != nil {
			return err
		}
	} else {
		if err := tx.Model(&remoteAddressEntry).Where("address = ?", remoteAddress).Update("attempts", remoteAddressEntry.Attempts+1).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLDatabase) registerSingleRecord(tx *gorm.DB, username, password, remoteAddress string, createdAt time.Time) error {
	if err := tx.Create(&AuthAttempt{
		Username:      username,
		Password:      password,
		RemoteAddress: remoteAddress,
		Base: Base{
			CreatedAt: createdAt,
		},
	}).Error; err != nil {
		return err
	}

	if err := s.upsertRemoteAddress(tx, remoteAddress); err != nil {
		return err
	}
	return nil
}

func (s *SQLDatabase) RegisterAuthAttept(username, password, remoteAddress string) error {
	err := s.conn.Transaction(func(tx *gorm.DB) error {
		return s.registerSingleRecord(tx, username, password, remoteAddress, time.Now().UTC())
	})

	return err
}

func (s *SQLDatabase) GetTopCountries(top int64) ([]params.Datapoint, error) {
	var data []params.Datapoint
	if err := s.conn.Raw(fmt.Sprintf("select country as name,COUNT(*) as count from remote_addresses group by name order by count DESC LIMIT %d", top)).Scan(&data).Error; err != nil {
		return nil, err
	}
	return data, nil
}

func (s *SQLDatabase) GetTopPasswords(top int64) ([]params.Datapoint, error) {
	var data []params.Datapoint
	if err := s.conn.Raw(fmt.Sprintf("select password as name,COUNT(*) as count from auth_attempts group by name order by count DESC LIMIT %d", top)).Scan(&data).Error; err != nil {
		return nil, err
	}
	return data, nil
}

func (s *SQLDatabase) GetTopUsers(top int64) ([]params.Datapoint, error) {
	var data []params.Datapoint
	if err := s.conn.Raw(fmt.Sprintf("select username as name,COUNT(*) as count from auth_attempts group by name order by count DESC LIMIT %d", top)).Scan(&data).Error; err != nil {
		return nil, err
	}
	return data, nil
}

func (s *SQLDatabase) GetLastAuthAttemptsByDay(days int64) ([]params.Datapoint, error) {
	var data []params.Datapoint
	if err := s.conn.Raw(fmt.Sprintf("select date(created_at) as name,COUNT(*) as count from auth_attempts where created_at > date('now', '-%d day') group by name order by name ASC", days)).Scan(&data).Error; err != nil {
		return nil, err
	}
	return data, nil
}

func (s *SQLDatabase) ImportFromCSV(csvFile string) error {
	err := s.conn.Transaction(func(tx *gorm.DB) error {
		csvFile, err := os.OpenFile(csvFile, os.O_RDONLY, 0644)
		if err != nil {
			return fmt.Errorf("failed to read csv file: %w", err)
		}
		defer csvFile.Close()

		reader := csv.NewReader(csvFile)
		header, err := reader.Read()
		if err != nil {
			return fmt.Errorf("failed to read csv file: %w", err)
		}
		if len(header) != 4 {
			return fmt.Errorf("invalid csv file format")
		}

		if header[0] != "date" || header[1] != "address" || header[2] != "username" || header[3] != "password" {
			return fmt.Errorf("invalid csv file format")
		}

		for {
			row, err := reader.Read()
			if err != nil {
				if err == io.EOF {
					break
				}
				continue
			}
			parsedDate, err := time.Parse("2006/01/02 15:04:05", row[0])
			if err != nil {
				return err
			}

			remoteAddr, err := net.ResolveUDPAddr("udp", row[1])
			if err != nil {
				fmt.Printf("failed to parse ip %s: %v\n", row[1], err)
				continue
			}

			if err := s.registerSingleRecord(tx, row[2], row[3], remoteAddr.IP.String(), parsedDate); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}
