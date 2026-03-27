package db

import (
	"log"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func Init(dsn string) {
	InitWithLogLevel(dsn, logger.Info)
}

func InitWithLogLevel(dsn string, level logger.LogLevel) {
	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(level),
	})
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}
	sqlDB, err := DB.DB()
	if err != nil {
		log.Fatalf("failed to get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetMaxIdleConns(10)
}
