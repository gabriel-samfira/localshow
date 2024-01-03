package database

import (
	"fmt"
	"net"

	"github.com/oschwald/geoip2-golang"
)

func newGeoIP(dbFile string) (*geoIP, error) {
	conn, err := geoip2.Open(dbFile)
	if err != nil {
		return nil, fmt.Errorf("opening geoip database: %w", err)
	}
	return &geoIP{
		dbFile: dbFile,
		conn:   conn,
	}, nil
}

type geoIP struct {
	dbFile string
	conn   *geoip2.Reader
}

func (g *geoIP) GetRecord(ip string) (*geoip2.City, error) {
	ipAddr := net.ParseIP(ip)
	if ipAddr == nil {
		return nil, fmt.Errorf("invalid IP address %s", ip)
	}
	return g.conn.City(net.ParseIP(ip))
}
