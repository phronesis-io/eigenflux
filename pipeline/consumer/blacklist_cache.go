package consumer

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	blacklistCacheKey = "cache:blacklist:keywords"
	blacklistCacheTTL = 60 * time.Second
)

// loadBlacklistKeywords returns all enabled blacklist keyword strings.
// Uses Redis cache with 60s TTL. On any failure, returns nil (skip check).
func loadBlacklistKeywords(ctx context.Context) []string {
	// Try Redis cache first
	cached, err := mq.RDB.Get(ctx, blacklistCacheKey).Result()
	if err == nil {
		var keywords []string
		if json.Unmarshal([]byte(cached), &keywords) == nil {
			return keywords
		}
	} else if err != redis.Nil {
		log.Printf("[Blacklist] Redis cache read error: %v", err)
	}

	// Cache miss: query DB
	keywords, err := getEnabledBlacklistKeywords(db.DB)
	if err != nil {
		log.Printf("[Blacklist] DB query error, skipping blacklist check: %v", err)
		return nil
	}

	// Write back to cache (best-effort)
	if data, err := json.Marshal(keywords); err == nil {
		if err := mq.RDB.Set(ctx, blacklistCacheKey, string(data), blacklistCacheTTL).Err(); err != nil {
			log.Printf("[Blacklist] Redis cache write error: %v", err)
		}
	}

	return keywords
}

// getEnabledBlacklistKeywords queries all enabled keywords from DB.
func getEnabledBlacklistKeywords(gormDB *gorm.DB) ([]string, error) {
	var keywords []string
	err := gormDB.Table("content_blacklist_keywords").
		Where("enabled = ?", true).
		Pluck("keyword", &keywords).Error
	return keywords, err
}
