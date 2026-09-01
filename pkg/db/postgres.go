package db

import (
	"os"
	"strings"

	"eigenflux_server/pkg/logger"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var DB *gorm.DB

// Init opens the shared connection with the GORM log level taken from
// DB_LOG_LEVEL (silent | error | warn | info). The default is Warn: at Info
// GORM prints every statement (three journal lines per query), which on
// 2026-09-01 made the production journal rotate 8 MiB every ~35 s and keep
// only ~35 minutes of history for every unit on the host.
func Init(dsn string) {
	InitWithLogLevel(dsn, LogLevelFromEnv())
}

// LogLevelFromEnv maps DB_LOG_LEVEL to a GORM log level; unset/unknown = Warn.
func LogLevelFromEnv() gormlogger.LogLevel {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DB_LOG_LEVEL"))) {
	case "silent":
		return gormlogger.Silent
	case "error":
		return gormlogger.Error
	case "info", "debug":
		return gormlogger.Info
	default:
		return gormlogger.Warn
	}
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
