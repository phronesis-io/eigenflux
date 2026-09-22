// Package discoveryserve owns delivery, response caching and best-effort
// exposure recording. Ranking and source hydration remain in Sort.
package discoveryserve

import (
	"context"
	"crypto/sha256"
	"eigenflux_server/pkg/discovery"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"strconv"
	"time"
)

type Executor interface {
	Execute(context.Context, int64, discovery.Request, discovery.Mode, int64) (discovery.Execution, error)
}
type Service struct {
	Redis        *redis.Client
	IDs          discovery.IDGenerator
	Executor     Executor
	StreamMaxLen int64
	DisableDedup bool
}
type cached struct {
	Hash     string             `json:"hash"`
	Response discovery.Response `json:"response"`
}

func (s Service) Serve(ctx context.Context, owner int64, r discovery.Request, mode discovery.Mode, key string) (discovery.Response, error) {
	empty := discovery.Response{}
	if owner <= 0 {
		return empty, discovery.Failure(401, "unauthorized")
	}
	if len(key) > 128 {
		return empty, discovery.Invalid("idempotency_key", "too_long")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := json.Marshal(struct {
		Mode    discovery.Mode
		Request discovery.Request
	}{mode, r})
	if err != nil {
		return empty, err
	}
	digest := sha256.Sum256(body)
	hash := hex.EncodeToString(digest[:])
	id, err := s.IDs.NextID()
	if err != nil {
		return empty, err
	}
	impression := strconv.FormatInt(id, 10)
	cacheKey := ""
	if key != "" {
		kh := sha256.Sum256([]byte(key))
		cacheKey = fmt.Sprintf("discovery:serve:%d:%x", owner, kh)
		load := func() (discovery.Response, bool, error) {
			raw, err := s.Redis.Get(ctx, cacheKey).Result()
			if err == redis.Nil {
				return empty, false, nil
			}
			if err != nil {
				return empty, false, err
			}
			var prior cached
			if err := json.Unmarshal([]byte(raw), &prior); err != nil {
				return empty, false, err
			}
			if prior.Hash != hash {
				return empty, true, discovery.Failure(409, "idempotency_conflict")
			}
			return prior.Response, true, nil
		}
		if response, ok, err := load(); err != nil || ok {
			return response, err
		}
		unlock, err := s.lock(ctx, cacheKey+":lock", impression)
		if err != nil {
			return empty, err
		}
		defer unlock()
		if response, ok, err := load(); err != nil || ok {
			return response, err
		}
	}
	now := time.Now().UnixMilli()
	x, err := s.Executor.Execute(ctx, owner, r, mode, now)
	if err != nil {
		return empty, err
	}
	if mode == "legacy_search" {
		mode = discovery.Search
	}
	x.Mode = mode
	response := responseFor(x, impression)
	if cacheKey != "" {
		raw, err := json.Marshal(cached{Hash: hash, Response: response})
		if err != nil {
			return empty, err
		}
		if err := s.Redis.Set(ctx, cacheKey, raw, 24*time.Hour).Err(); err != nil {
			return empty, err
		}
	}
	s.recordAsync(ctx, owner, x, impression, 0, now)
	return response, nil
}

func responseFor(x discovery.Execution, impression string) discovery.Response {
	response := discovery.Response{RequestID: impression, ImpressionID: impression, Mode: x.Mode, PipelineVersion: discovery.PipelineVersion, Items: []discovery.ResultItem{}, Status: x.Status, Partial: len(x.PartialReasons) > 0, Reasons: x.PartialReasons, FallbackReason: x.FallbackReason, ConstraintMode: "explicit_filters"}
	if len(x.Contexts) > 0 {
		c := x.Contexts[0]
		if len(x.Candidates) > 0 {
			c = x.Candidates[0].Context
		}
		if c.Need != nil {
			response.ConstraintMode = "structured_need"
		}
		response.Origin = c.Origin
		response.ContextID = c.ID
		response.EffectiveFilters = c.Filters
	}
	for _, c := range x.Candidates {
		response.Items = append(response.Items, discovery.PublicItem(c))
	}
	return response
}

// The lock protects only response/page cache state, never recording side effects.
func (s Service) lock(ctx context.Context, key, token string) (func(), error) {
	locked, err := s.Redis.SetNX(ctx, key, token, 60*time.Second).Result()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, discovery.Failure(409, "request_in_progress")
	}
	return func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer done()
		s.Redis.Eval(cleanup, `if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`, []string{key}, token)
	}, nil
}
