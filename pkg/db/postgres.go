package db

import (
	"os"

	"eigenflux_server/pkg/logger"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var DB *gorm.DB

func Init(dsn string) {
	InitWithLogLevel(dsn, gormlogger.Info)
}

func InitWithLogLevel(dsn string, level gormlogger.LogLevel) {
	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(level),
	})
	if err != nil {
		logger.Default().Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	sqlDB, err := DB.DB()
	if err != nil {
		logger.Default().Error("failed to get sql.DB", "err", err)
		os.Exit(1)
	}
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetMaxIdleConns(10)
}
