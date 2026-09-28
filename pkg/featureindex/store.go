package featureindex

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"eigenflux_server/pkg/metrics"
	"github.com/redis/go-redis/v9"
)

// Forward stores registered versioned components. Namespace isolates projection
// generations. Logical freshness expiry retains the write fence for late writers.
type Forward struct {
	Redis     *redis.Client
	Namespace string
	Registry  *Registry // nil uses the process-wide hot-reloaded registry
}

func (f Forward) Key(id int64, component string) string {
	return fmt.Sprintf("discovery:forward:%s:%d:%s", f.Namespace, id, component)
}

// Compare decimal versions as strings: Lua numbers cannot represent every int64.
const putForwardLua = `
local old = redis.call('HGET', KEYS[1], 'version')
local next = ARGV[1]
if old and (#old > #next or (#old == #next and old > next)) then return 0 end
redis.call('HSET', KEYS[1], 'version', next, 'data', ARGV[2], 'expires_at', ARGV[3])
if tonumber(ARGV[4]) > 0 then redis.call('PEXPIRE', KEYS[1], ARGV[4]) else redis.call('PERSIST', KEYS[1]) end
return 1
`

var putForward = redis.NewScript(putForwardLua)

// Mutation describes one independently versioned component write.
type Mutation struct {
	ID        int64
	Component string
	Version   int64
	Value     any
}

func (f Forward) Put(ctx context.Context, id int64, component string, version int64, value any) error {
	return f.PutBatch(ctx, []Mutation{{id, component, version, value}})
}

// PutBatch validates/encodes the whole batch before issuing bounded, pipelined
// writes. Each key keeps its own version fence; a failed page can be retried.
func (f Forward) PutBatch(ctx context.Context, values []Mutation) (resultErr error) {
	if len(values) == 0 {
		return nil
	}
	if f.Redis == nil || len(values) > 2000 {
		return fmt.Errorf("invalid forward batch")
	}
	type encoded struct {
		m    Mutation
		view Definition
		args []any
		cmd  *redis.Cmd
	}
	rows := make([]encoded, 0, len(values))
	for _, m := range values {
		if m.ID <= 0 || m.Version < 0 {
			return fmt.Errorf("invalid forward identity/version")
		}
		view, err := f.registry().resolve(f.Namespace, m.Component)
		if err != nil {
			return err
		}
		b, err := view.project(m.ID, m.Version, m.Value)
		if err != nil {
			return err
		}
		rows = append(rows, encoded{m: m, view: view, args: []any{strconv.FormatInt(m.Version, 10), b, expiry(view.TTL), view.RetentionTTL.Milliseconds()}})
	}

	pipe := f.Redis.Pipeline()
	for i := range rows {
		row := &rows[i]
		row.cmd = pipe.EvalSha(ctx, putForward.Hash(), []string{f.Key(row.m.ID, row.m.Component)}, row.args...)
	}
	_, _ = pipe.Exec(ctx)
	// Redis may evict its script cache. Retry only NOSCRIPT commands with EVAL;
	// do not repeat accepted writes or swallow transport/server failures.
	retry := f.Redis.Pipeline()
	needRetry := false
	for i := range rows {
		row := &rows[i]
		if redis.HasErrorPrefix(row.cmd.Err(), "NOSCRIPT") {
			row.cmd = retry.Eval(ctx, putForwardLua, []string{f.Key(row.m.ID, row.m.Component)}, row.args...)
			needRetry = true
		}
	}
	if needRetry {
		_, _ = retry.Exec(ctx)
	}
	for _, row := range rows {
		accepted, err := row.cmd.Int()
		if err == nil {
			requestCacheFrom(ctx).remove(requestKey{client: f.Redis, namespace: f.Namespace, component: row.m.Component, id: row.m.ID, plans: row.view.plans})
		}
		outcome := "stored"
		if err != nil {
			outcome = "error"
			if resultErr == nil {
				resultErr = err
			}
		} else if accepted == 0 {
			outcome = "stale"
		}
		metrics.FeatureWriteItems.WithLabelValues(row.view.Name, outcome).Inc()
	}
	return resultErr
}

type componentRows struct {
	view Definition
	rows map[int64]json.RawMessage
}

// ReadRequest describes a generation-specific batch; component revisions remain independent.
type ReadRequest struct {
	Forward    Forward
	IDs        []int64
	Components []string
}

// Prefetch warms only the request cache, using one Redis pipeline across types.
func Prefetch(ctx context.Context, requests []ReadRequest) error {
	if requestCacheFrom(ctx) == nil {
		return fmt.Errorf("feature prefetch requires request cache")
	}
	_, err := readRequests(ctx, requests)
	return err
}
func (f Forward) readComponents(ctx context.Context, ids []int64, components ...string) (map[string]componentRows, error) {
	rows, err := readRequests(ctx, []ReadRequest{{f, ids, components}})
	if err != nil {
		return nil, err
	}
	return rows[0], nil
}

func readRequests(ctx context.Context, requests []ReadRequest) (out []map[string]componentRows, resultErr error) {
	out = make([]map[string]componentRows, len(requests))
	type pending struct {
		id     int64
		view   string
		entity string
		rows   map[int64]json.RawMessage
		cmd    *redis.SliceCmd
		key    requestKey
	}
	var client *redis.Client
	var pipe redis.Pipeliner
	queue := []pending{}
	cache := requestCacheFrom(ctx)
	for ri, req := range requests {
		if len(req.IDs) > 1000 {
			return nil, fmt.Errorf("feature read bound exceeded")
		}
		unique := make([]int64, 0, len(req.IDs))
		seen := map[int64]bool{}
		for _, id := range req.IDs {
			if id <= 0 {
				return nil, fmt.Errorf("invalid feature entity ID")
			}
			if !seen[id] {
				seen[id] = true
				unique = append(unique, id)
			}
		}
		if len(unique) > 0 {
			if req.Forward.Redis == nil {
				return nil, fmt.Errorf("feature Redis required")
			}
			if client == nil {
				client = req.Forward.Redis
				pipe = client.Pipeline()
			} else if client != req.Forward.Redis {
				return nil, fmt.Errorf("feature batch spans Redis clients")
			}
		}
		out[ri] = map[string]componentRows{}
		for _, component := range req.Components {
			view, err := req.Forward.registry().resolve(req.Forward.Namespace, component)
			if err != nil {
				return nil, err
			}
			row := componentRows{view: view, rows: map[int64]json.RawMessage{}}
			out[ri][component] = row
			for _, id := range unique {
				key := requestKey{client: client, namespace: req.Forward.Namespace, component: component, id: id, plans: view.plans}
				if raw, ok := cache.get(key); ok {
					if raw != nil {
						row.rows[id] = raw
					}
					metrics.FeatureReadItems.WithLabelValues(view.Entity, view.Name, "request_hit").Inc()
					continue
				}
				queue = append(queue, pending{id, view.Name, view.Entity, row.rows, pipe.HMGet(ctx, req.Forward.Key(id, component), "data", "expires_at"), key})
			}
		}
	}

	if len(queue) > 4000 {
		return nil, fmt.Errorf("feature component batch bound exceeded")
	}
	if len(queue) > 0 {
		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			for _, p := range queue {
				metrics.FeatureReadItems.WithLabelValues(p.entity, p.view, "error").Inc()
			}
			return nil, err
		}
	}
	now := time.Now().UnixMilli()
	for _, p := range queue {
		values, err := p.cmd.Result()
		if err != nil {
			return nil, err
		}
		if values[0] == nil {
			metrics.FeatureReadItems.WithLabelValues(p.entity, p.view, "miss").Inc()
			cache.put(p.key, nil)
			continue
		}
		if values[1] != nil {
			until, err := strconv.ParseInt(values[1].(string), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid feature expiry: %w", err)
			}
			if until > 0 && until <= now {
				metrics.FeatureReadItems.WithLabelValues(p.entity, p.view, "expired").Inc()
				cache.put(p.key, nil)
				continue
			}
		}
		raw := json.RawMessage(values[0].(string))
		p.rows[p.id] = raw
		cache.put(p.key, raw)
		metrics.FeatureReadItems.WithLabelValues(p.entity, p.view, "hit").Inc()
	}
	return out, nil
}

// Get retains the generic JSON API; typed domain readers decode directly once.
func (f Forward) Get(ctx context.Context, ids []int64, component string) (map[int64]json.RawMessage, error) {
	views, err := f.readComponents(ctx, ids, component)
	if err != nil {
		return nil, err
	}
	view := views[component]
	out := make(map[int64]json.RawMessage, len(view.rows))
	for id, raw := range view.rows {
		selected, err := view.view.selectFields(raw)
		if err != nil {
			return nil, err
		}
		out[id], err = json.Marshal(selected)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func expiry(ttl time.Duration) int64 {
	if ttl == 0 {
		return 0
	}
	return time.Now().Add(ttl).UnixMilli()
}

// AllocateVersion fences a source read before it starts. Redis TIME seeds the
// sequence so recovery after loss of the counter does not restart at version 1.
var allocateVersion = redis.NewScript(`
local t = redis.call('TIME')
local base = tonumber(t[1]) * 1000000 + tonumber(t[2])
local prior = tonumber(redis.call('GET', KEYS[1]) or '0')
local next = math.max(base, prior + 1)
redis.call('SET', KEYS[1], string.format('%.0f', next))
return string.format('%.0f', next)
`)

func (f Forward) AllocateVersion(ctx context.Context) (int64, error) {
	if f.Redis == nil {
		return 0, fmt.Errorf("feature Redis is required")
	}
	raw, err := allocateVersion.Run(ctx, f.Redis, []string{"discovery:feature:read_fence"}).Text()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(raw, 10, 64)
}

func (f Forward) registry() *Registry {
	if f.Registry != nil {
		return f.Registry
	}
	return defaultRegistry
}
