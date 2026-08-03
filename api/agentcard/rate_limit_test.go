package agentcardapi

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestAllowFixedWindow(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < 2; i++ {
		if !allowFixedWindow(ctx, rdb, 42, "write", 2, time.Minute, now) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if allowFixedWindow(ctx, rdb, 42, "write", 2, time.Minute, now) {
		t.Fatal("request above the limit should be rejected")
	}
	if !allowFixedWindow(ctx, rdb, 42, "read", 2, time.Minute, now) {
		t.Fatal("different scopes must use independent counters")
	}
	if !allowFixedWindow(ctx, rdb, 42, "write", 2, time.Minute, now.Add(time.Minute)) {
		t.Fatal("a new fixed window should reset the counter")
	}
}

func TestAllowFixedWindowFailsOpenWithoutRedis(t *testing.T) {
	if !allowFixedWindow(context.Background(), nil, 42, "write", 1, time.Minute, time.Now()) {
		t.Fatal("a Redis outage must not take the endpoint down")
	}
}
