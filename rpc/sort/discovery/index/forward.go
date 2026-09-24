package index

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// Forward stores versioned projection components, without TTL. Namespace names
// the concrete ES generation so a backfill cannot alter the live generation.
type Forward struct {
	Redis     *redis.Client
	Namespace string
}

func (f Forward) Key(id int64, component string) string {
	return fmt.Sprintf("discovery:forward:%s:%d:%s", f.Namespace, id, component)
}

// Compare decimal versions as strings: Lua numbers cannot represent every int64.
var putForward = redis.NewScript(`
local old = redis.call('HGET', KEYS[1], 'version')
local next = ARGV[1]
if old and (#old > #next or (#old == #next and old > next)) then return 0 end
redis.call('HSET', KEYS[1], 'version', next, 'data', ARGV[2])
return 1
`)

func (f Forward) Put(ctx context.Context, id int64, component string, version int64, value any) error {
	if f.Redis == nil || f.Namespace == "" || id <= 0 || version < 0 || component == "" {
		return fmt.Errorf("invalid forward projection configuration")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return putForward.Run(ctx, f.Redis, []string{f.Key(id, component)}, strconv.FormatInt(version, 10), b).Err()
}

// Get batches one component for at most 100 candidates. Absent rows are omitted;
// Redis failures are errors, never indistinguishable from legitimate misses.
func (f Forward) Get(ctx context.Context, ids []int64, component string) (map[int64]json.RawMessage, error) {
	out := map[int64]json.RawMessage{}
	if len(ids) == 0 {
		return out, nil
	}
	if f.Redis == nil || f.Namespace == "" || len(ids) > 100 {
		return nil, fmt.Errorf("invalid forward read configuration")
	}
	pipe := f.Redis.Pipeline()
	cmds := map[int64]*redis.StringCmd{}
	for _, id := range ids {
		cmds[id] = pipe.HGet(ctx, f.Key(id, component), "data")
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}
	for id, cmd := range cmds {
		b, err := cmd.Bytes()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, nil
}
