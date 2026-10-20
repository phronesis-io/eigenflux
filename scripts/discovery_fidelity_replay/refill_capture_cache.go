package main

import (
	"context"
	"eigenflux_server/rpc/sort/discovery"
	"fmt"
	"github.com/redis/go-redis/v9"
	"sync"
)

// Freeze shared item projections/claims on first observation during this capture
// process. Owner-specific source visibility and rolling consumption are never
// cached here. This is private replay input storage, not a serving cache.
var captureFacts = struct {
	sync.Mutex
	forward map[string]map[string]string
	claims  map[int64]bool
}{forward: map[string]map[string]string{}, claims: map[int64]bool{}}

const maxCaptureFacts = 100000

func captureForward(ctx context.Context, remote *redis.Client, docs []discovery.Document) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	pipe := remote.Pipeline()
	missing := map[string]*redis.MapStringStringCmd{}
	captureFacts.Lock()
	for _, d := range docs {
		key := fmt.Sprintf("discovery:forward:broadcast:v1:%d:item", d.Ref.ID)
		if value, ok := captureFacts.forward[key]; ok {
			out[key] = value
		} else {
			missing[key] = pipe.HGetAll(ctx, key)
		}
	}
	captureFacts.Unlock()
	if len(missing) > 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, err
		}
	}
	captureFacts.Lock()
	defer captureFacts.Unlock()
	for key, cmd := range missing {
		value, ok := captureFacts.forward[key]
		if !ok {
			value = cmd.Val()
			if len(captureFacts.forward) < maxCaptureFacts {
				captureFacts.forward[key] = value
			}
		}
		out[key] = value
	}
	return out, nil
}
func captureClaims(ctx context.Context, remote *redis.Client, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	pipe := remote.Pipeline()
	missing := map[int64]*redis.IntCmd{}
	captureFacts.Lock()
	for _, id := range ids {
		if value, ok := captureFacts.claims[id]; ok {
			out[id] = value
		} else {
			missing[id] = pipe.Exists(ctx, fmt.Sprintf("sort:inject:claim:%d", id))
		}
	}
	captureFacts.Unlock()
	if len(missing) > 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, err
		}
	}
	captureFacts.Lock()
	defer captureFacts.Unlock()
	for id, cmd := range missing {
		value, ok := captureFacts.claims[id]
		if !ok {
			value = cmd.Val() > 0
			if len(captureFacts.claims) < maxCaptureFacts {
				captureFacts.claims[id] = value
			}
		}
		out[id] = value
	}
	return out, nil
}
