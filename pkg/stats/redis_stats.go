package stats

import (
	"context"
	"eigenflux_server/pkg/logger"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

const (
	// Redis keys for statistics
	KeyAgentCount       = "stats:agent_count"
	KeyItemTotal        = "stats:item_total"
	KeyHighQualityCount = "stats:high_quality_count"
	KeyAgentCountries   = "stats:agent_countries"
)

// Stats holds website statistics
type Stats struct {
	AgentCount           int64
	ItemCount            int64
	HighQualityItemCount int64
	AgentCountries       []string
}

// IncrItemTotal increments the total item count
func IncrItemTotal(ctx context.Context, rdb *redis.Client) error {
	return rdb.Incr(ctx, KeyItemTotal).Err()
}

// IncrHighQualityCount increments the high-quality item count
func IncrHighQualityCount(ctx context.Context, rdb *redis.Client) error {
	return rdb.Incr(ctx, KeyHighQualityCount).Err()
}

// SetAgentCount sets the agent count
func SetAgentCount(ctx context.Context, rdb *redis.Client, count int64) error {
	return rdb.Set(ctx, KeyAgentCount, count, 0).Err()
}

// SetItemTotal sets the item total count (for calibration)
func SetItemTotal(ctx context.Context, rdb *redis.Client, count int64) error {
	return rdb.Set(ctx, KeyItemTotal, count, 0).Err()
}

// SetHighQualityCount sets the high-quality item count (for calibration)
func SetHighQualityCount(ctx context.Context, rdb *redis.Client, count int64) error {
	return rdb.Set(ctx, KeyHighQualityCount, count, 0).Err()
}

// AddAgentCountry adds a country to the agent countries set (write-time incremental sync)
func AddAgentCountry(ctx context.Context, rdb *redis.Client, country string) error {
	country = strings.TrimSpace(country)
	if country == "" {
		return nil
	}
	return rdb.SAdd(ctx, KeyAgentCountries, country).Err()
}

// GetAgentCountries returns all agent countries from Redis
func GetAgentCountries(ctx context.Context, rdb *redis.Client) ([]string, error) {
	countries, err := rdb.SMembers(ctx, KeyAgentCountries).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("failed to get agent countries: %w", err)
	}
	sort.Strings(countries)
	return countries, nil
}

// CalibrateAgentCountries atomically replaces the agent countries set
func CalibrateAgentCountries(ctx context.Context, rdb *redis.Client, countries []string) error {
	tmpKey := KeyAgentCountries + "_tmp"

	pipe := rdb.Pipeline()
	pipe.Del(ctx, tmpKey)
	if len(countries) > 0 {
		members := make([]interface{}, len(countries))
		for i, c := range countries {
			members[i] = c
		}
		pipe.SAdd(ctx, tmpKey, members...)
		pipe.Rename(ctx, tmpKey, KeyAgentCountries)
	} else {
		pipe.Del(ctx, KeyAgentCountries)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// GetStats retrieves all statistics from Redis
func GetStats(ctx context.Context, rdb *redis.Client) (*Stats, error) {
	stats := &Stats{}

	// Get agent count
	agentCountStr, err := rdb.Get(ctx, KeyAgentCount).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("failed to get agent count: %w", err)
	}
	if err != redis.Nil {
		stats.AgentCount, _ = strconv.ParseInt(agentCountStr, 10, 64)
	}

	// Get item total
	itemTotalStr, err := rdb.Get(ctx, KeyItemTotal).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("failed to get item total: %w", err)
	}
	if err != redis.Nil {
		stats.ItemCount, _ = strconv.ParseInt(itemTotalStr, 10, 64)
	}

	// Get high-quality count
	hqCountStr, err := rdb.Get(ctx, KeyHighQualityCount).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("failed to get high quality count: %w", err)
	}
	if err != redis.Nil {
		stats.HighQualityItemCount, _ = strconv.ParseInt(hqCountStr, 10, 64)
	}

	// Get agent countries
	stats.AgentCountries, _ = GetAgentCountries(ctx, rdb)

	return stats, nil
}

// InitializeStats initializes statistics from database and Elasticsearch
// This should be called on startup
func InitializeStats(ctx context.Context, rdb *redis.Client, agentCount, itemCount, hqCount int64) error {
	logger.Default().Info("initializing stats", "agents", agentCount, "items", itemCount, "highQuality", hqCount)

	if err := SetAgentCount(ctx, rdb, agentCount); err != nil {
		return fmt.Errorf("failed to set agent count: %w", err)
	}

	if err := SetItemTotal(ctx, rdb, itemCount); err != nil {
		return fmt.Errorf("failed to set item total: %w", err)
	}

	if err := SetHighQualityCount(ctx, rdb, hqCount); err != nil {
		return fmt.Errorf("failed to set high quality count: %w", err)
	}

	logger.Default().Info("stats initialized successfully")
	return nil
}
