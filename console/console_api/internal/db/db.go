package db

import (
	"os"

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

func InitPostgres(dsn string) {
	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Info),
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
