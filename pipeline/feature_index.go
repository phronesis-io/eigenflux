package main

import (
	"fmt"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/featureindex"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Feature retention follows the enabled consumers, independently of routing.
// Legacy Commission discovery also reads these expiring projections.
func featureLoaders(cfg *config.Config, db *gorm.DB, r *redis.Client, source featureindex.CommissionSource) (*featureindex.Loaders, error) {
	indices := []featureindex.Materializer{}
	if cfg.EnableCommissionIndex {
		if source == nil {
			return nil, fmt.Errorf("Commission feature source is required")
		}
		indices = append(indices, featureindex.CommissionIndex{Source: source, Redis: r, IndexName: cfg.CommissionIndexName})
	}
	if cfg.EnableNeedSearch {
		indices = append(indices, featureindex.BroadcastIndex{DB: db, Redis: r}, featureindex.AgentIndex{DB: db, Redis: r, IndexName: cfg.AgentDiscoveryIndex})
	}
	if len(indices) == 0 {
		return nil, nil
	}
	loaders := &featureindex.Loaders{Redis: r}
	for _, index := range indices {
		if err := loaders.Register(featureindex.Loader{Index: index, Interval: time.Second, Timeout: 20 * time.Second, BatchSize: 100, CyclePause: 5 * time.Minute, DynamicConfig: true}); err != nil {
			return nil, err
		}
	}
	return loaders, nil
}
