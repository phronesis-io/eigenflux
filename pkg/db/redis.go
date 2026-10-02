package db

import (
	"eigenflux_server/pkg/cache"
	"github.com/redis/go-redis/v9"
)

var RDB *redis.Client

func InitRedis(addr, password string) {
	RDB = cache.SharedConnections.Client(addr, password)
}
