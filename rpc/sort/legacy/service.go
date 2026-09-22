package legacy

import (
	"context"
	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/recall"
	"eigenflux_server/pkg/recallsource"
	"eigenflux_server/rpc/sort/lrranker"
	"eigenflux_server/rpc/sort/ranker"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"
)

// Service owns the existing recommendation pipeline and its process-local state.
type Service struct {
	db                 *gorm.DB
	redis              *redis.Client
	bf                 *bloomfilter.BloomFilter
	cfg                *config.Config
	searchCache        *cache.SearchCache
	profileCache       *cache.ProfileCache
	rankerInstance     *ranker.Ranker
	rankerCfg          *ranker.RankerConfig
	lrManager          *lrranker.Manager
	itemRerankPolicies *rerankPolicySet
	embeddingCache     *cache.EmbeddingCache
	recallSources      []recallsource.RecallSource
	sfGroup            singleflight.Group
	contentClassCache  *expirable.LRU[int64, bool]
}

func New(cfg *config.Config, database *gorm.DB, redisClient *redis.Client) *Service {
	s := &Service{cfg: cfg, db: database, redis: redisClient, contentClassCache: expirable.NewLRU[int64, bool](pgcClassCacheSize, nil, pgcClassCacheTTL)}
	// Initialize Bloom Filter (for group_id deduplication)
	s.bf = bloomfilter.NewBloomFilter(s.redis)

	// Initialize cache
	if s.cfg.EnableSearchCache {
		s.searchCache = cache.NewSearchCache(
			s.redis,
			time.Duration(s.cfg.SearchCacheTTL)*time.Second,
			time.Duration(s.cfg.SearchCacheTTL)*time.Second,
		)
		s.profileCache = cache.NewProfileCache(
			s.redis,
			time.Duration(s.cfg.ProfileCacheTTL)*time.Second,
		)
		logger.Default().Info("cache enabled", "searchTTL", s.cfg.SearchCacheTTL, "profileTTL", s.cfg.ProfileCacheTTL)
	}

	// Initialize ranker
	s.rankerCfg = ranker.NewRankerConfig(s.cfg)
	s.rankerInstance = ranker.New(s.rankerCfg)
	s.itemRerankPolicies = loadRerankPolicySet(context.Background(), "configs/sort/rerank.yaml", time.Now)

	// Initialize the LR ranker. When enabled and a valid model is present, it
	// replaces the formula ordering of eligible items with the model's
	// follow-up probability; otherwise sort transparently falls back to the
	// formula ranker. The bundle is delivered to a local directory out-of-band.
	lrReload, err := time.ParseDuration(s.cfg.LRRankerReloadInterval)
	if err != nil {
		lrReload = 60 * time.Second
	}
	s.lrManager = lrranker.NewManager(lrranker.Config{
		Enabled:        s.cfg.LRRankerEnabled,
		ModelPath:      s.cfg.LRRankerModelPath,
		ReloadInterval: lrReload,
	})

	// Initialize embedding cache
	s.embeddingCache = cache.NewEmbeddingCache(s.redis, 24*time.Hour)

	// Initialize recall sources
	recallReader := recall.NewRedisRecallReader(s.redis, s.cfg.RecallRedisNamespace)
	if s.cfg.EnableHotRecall {
		s.recallSources = append(s.recallSources, recallsource.NewRedisRecallSource(recallReader, "hot_recall", recallsource.HotRecall, "hot_recall"))
	}
	if s.cfg.EnableNewRecall {
		s.recallSources = append(s.recallSources, recallsource.NewRedisRecallSource(recallReader, "new_recall", recallsource.NewRecall, "new_recall"))
	}
	if s.cfg.EnableNewUGCRecall {
		s.recallSources = append(s.recallSources, recallsource.NewRedisRecallSource(recallReader, "new_ugc_recall", recallsource.NewUGC, "new_ugc_recall"))
	}
	if s.cfg.EnableSwingI2IRecall {
		surfaceHistory := recall.NewSurfaceHistoryStore(s.redis, s.cfg.RecallRedisNamespace)
		s.recallSources = append(s.recallSources, recallsource.NewSwingI2IRecallSource(recallReader, surfaceHistory, s.redis, s.cfg.SwingI2IRecallSeeds, s.cfg.SwingI2IRecallK))
	}
	logger.Default().Info("recall sources initialized", "count", len(s.recallSources))

	return s
}
func (s *Service) Close() { s.lrManager.Close() }
