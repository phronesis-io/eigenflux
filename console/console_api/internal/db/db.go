package db

import (
	"os"
	"strings"

	"console.eigenflux.ai/internal/logger"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var (
	DB  *gorm.DB
	RDB *redis.Client
)

// gormLogLevel honours the same DB_LOG_LEVEL contract as the core services
// (silent | error | warn | info, debug = info). Console is deployed
// separately and keeps its historical default of Info when the variable is
// unset; core services default to Warn (see pkg/db).
func gormLogLevel() gormlogger.LogLevel {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DB_LOG_LEVEL"))) {
	case "silent":
		return gormlogger.Silent
	case "error":
		return gormlogger.Error
	case "warn":
		return gormlogger.Warn
	case "info", "debug", "":
		return gormlogger.Info
	default:
		return gormlogger.Info
	}
}

func InitPostgres(dsn string) {
	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormLogLevel()),
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

func InitRedis(addr, password string) {
	RDB = redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
	})
}
