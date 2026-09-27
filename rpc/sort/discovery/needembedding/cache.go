// Package needembedding owns versioned Need query vectors and precomputation state.
package needembedding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/embeddingmeta"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/rpc/sort/discovery/queryprocessing"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

var (
	ErrPending = errors.New("need embedding pending")
	ErrInvalid = errors.New("invalid need embedding")
	ErrBusy    = errors.New("need embedding generation already running")
)

const CacheTTL = 30 * 24 * time.Hour
const RefreshInterval = 24 * time.Hour
const LeaseDuration = time.Minute

type Profile struct {
	Provider   string
	Model      string
	Revision   string
	Endpoint   string
	Dimensions int
	Processor  string
}

type Cache struct {
	DB      *gorm.DB
	Redis   redis.UniversalClient
	Profile Profile
}

func New(cfg *config.Config, db *gorm.DB, redis redis.UniversalClient) *Cache {
	provider := embeddingmeta.NormalizeProvider(cfg.EmbeddingProvider)
	endpoint := strings.TrimRight(cfg.EmbeddingBaseURL, "/")
	if endpoint == "" {
		if provider == embeddingmeta.ProviderOllama {
			endpoint = "http://localhost:11434"
		} else {
			endpoint = "https://api.openai.com/v1"
		}
	}
	return &Cache{DB: db, Redis: redis, Profile: Profile{Provider: provider, Model: embeddingmeta.ResolveModel(provider, cfg.EmbeddingModel), Revision: cfg.DiscoveryEmbeddingRevision, Endpoint: endpoint, Dimensions: cfg.EmbeddingDimensions, Processor: queryprocessing.Version}}
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (c *Cache) Generation() string { return digest(c.Profile) }
func (c *Cache) Key(text string) string {
	return "discovery:need_embedding:v1:" + digest(struct{ TextHash, Generation string }{digest(text), c.Generation()})
}

func (c *Cache) valid(v []float32) bool {
	if c.Profile.Dimensions <= 0 || len(v) != c.Profile.Dimensions {
		return false
	}
	norm := 0.0
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
		norm += f * f
	}
	return norm > 0
}
func (c *Cache) Read(ctx context.Context, text string) ([]float32, error) {
	raw, err := c.Redis.Get(ctx, c.Key(text)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrPending
	}
	if err != nil {
		return nil, err
	}
	var v []float32
	if json.Unmarshal(raw, &v) != nil || !c.valid(v) {
		return nil, ErrInvalid
	}
	return v, nil
}

// Lookup never calls a model. A miss requests background work without resetting
// a failed job's backoff or stealing a running worker's lease.
func (c *Cache) Lookup(ctx context.Context, id int64, text, processor string) (vector []float32, resultErr error) {
	defer func() {
		outcome := "hit"
		if errors.Is(resultErr, ErrPending) {
			outcome = "pending"
		} else if resultErr != nil {
			outcome = "error"
		}
		metrics.DiscoveryNeedEmbedding.WithLabelValues("lookup", outcome).Inc()
	}()
	if processor != c.Profile.Processor {
		return nil, fmt.Errorf("query processor version mismatch")
	}
	v, err := c.Read(ctx, text)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, ErrPending) && !errors.Is(err, ErrInvalid) {
		return nil, err
	}
	now := time.Now().UnixMilli()
	err = c.DB.WithContext(ctx).Exec(`INSERT INTO need_embedding_jobs(need_input_id,generation,next_attempt_at)
 SELECT need_input_id,?,0 FROM current_need_inputs WHERE need_input_id=?
 ON CONFLICT(need_input_id,generation) DO UPDATE SET next_attempt_at=0,ready=false
 WHERE need_embedding_jobs.ready AND need_embedding_jobs.lease_until<=?`, c.Generation(), id, now).Error
	if err != nil {
		return nil, err
	}
	return nil, ErrPending
}

type Embedder interface {
	GetEmbedding(context.Context, string) ([]float32, error)
}

// Produce is worker-only. Identical processed text shares a vector across Needs.
func (c *Cache) Produce(ctx context.Context, text string, embedder Embedder) (vector []float32, resultErr error) {
	outcome := "reused"
	defer func() {
		if errors.Is(resultErr, ErrBusy) {
			outcome = "busy"
		} else if resultErr != nil {
			outcome = "error"
		}
		metrics.DiscoveryNeedEmbedding.WithLabelValues("produce", outcome).Inc()
	}()
	v, err := c.Read(ctx, text)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, ErrPending) && !errors.Is(err, ErrInvalid) {
		return nil, err
	}
	token := rand.Text()
	lock := c.Key(text) + ":lock"
	acquired, err := c.Redis.SetNX(ctx, lock, token, LeaseDuration).Result()
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, ErrBusy
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		c.Redis.Eval(cleanup, `if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`, []string{lock}, token)
	}()
	v, err = c.Read(ctx, text)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, ErrPending) && !errors.Is(err, ErrInvalid) {
		return nil, err
	}
	outcome = "generated"
	v, err = embedder.GetEmbedding(ctx, text)
	if err != nil {
		return nil, err
	}
	if !c.valid(v) {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if err = c.Redis.Set(ctx, c.Key(text), raw, CacheTTL).Err(); err != nil {
		return nil, err
	}
	return v, nil
}
