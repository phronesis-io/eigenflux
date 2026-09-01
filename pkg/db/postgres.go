package db

import (
	"io"
	"log"
	"os"
	"strings"
	"time"

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

// NewGormLogger builds the statement logger used by every core service:
// slow threshold 200 ms (GORM default), no ANSI colours (the sink is journald,
// not a terminal), and ErrRecordNotFound is NOT logged — a "row missing" is an
// ordinary answer for lookups such as session-by-token, and logging it would
// let any stream of invalid bearer tokens flood the journal even at Warn.
func NewGormLogger(level gormlogger.LogLevel) gormlogger.Interface {
	return NewGormLoggerTo(os.Stdout, level)
}

// NewGormLoggerTo is NewGormLogger with an explicit sink (tests).
func NewGormLoggerTo(w io.Writer, level gormlogger.LogLevel) gormlogger.Interface {
	return gormlogger.New(
		log.New(w, "\r\n", log.LstdFlags),
		gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  level,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)
}

func InitWithLogLevel(dsn string, level gormlogger.LogLevel) {
	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: NewGormLogger(level),
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
