package database

import (
	"time"

	"gorm.io/gorm"
)

type Base struct {
	CreatedAt time.Time `gorm:"index"`
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

type AuthAttempt struct {
	Base

	Username      string `gorm:"index:auth_username;index:cred_username"`
	Password      string `gorm:"index:auth_password;index:cred_password"`
	RemoteAddress string `gorm:"index:auth_remote_address"`
}

type RemoteAddress struct {
	Base

	Address  string `gorm:"uniqueIndex:remote_address"`
	Attempts int64
	Country  string `gorm:"index:remote_country"`
	City     string `gorm:"index:remote_city"`
}
