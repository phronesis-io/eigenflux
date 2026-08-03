package agentcardapi

import (
	"context"
	"encoding/json"
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
	mr.SetTime(now)
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
	mr.FastForward(time.Minute + time.Second)
	if !allowFixedWindow(ctx, rdb, 42, "write", 2, time.Minute, now.Add(time.Minute)) {
		t.Fatal("requests should be allowed after the rolling window expires")
	}
}

func TestJSONValuesEqualIgnoresObjectKeyOrder(t *testing.T) {
	if !jsonValuesEqual(json.RawMessage(`{"a":1,"b":["x"]}`), json.RawMessage(`{"b":["x"],"a":1}`)) {
		t.Fatal("semantically equal JSON values should be treated as a no-op")
	}
	if jsonValuesEqual(json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":2}`)) {
		t.Fatal("different JSON values must remain an actual change")
	}
}

func TestAllowFixedWindowFailsOpenWithoutRedis(t *testing.T) {
	if !allowFixedWindow(context.Background(), nil, 42, "write", 1, time.Minute, time.Now()) {
		t.Fatal("a Redis outage must not take the endpoint down")
	}
}

func TestCheckFixedWindowReportsRedisFailure(t *testing.T) {
	allowed, err := checkFixedWindow(context.Background(), nil, 42, "write", 1, time.Minute, time.Now())
	if err == nil || allowed {
		t.Fatalf("checkFixedWindow(nil) = (%v, %v), want unavailable", allowed, err)
	}
}

func TestCheckProfileWriteRateChecksBothQuotasAtomically(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	now := time.Unix(1_800_000_000, 0)
	mr.SetTime(now)
	for i := 0; i < int(profileWriteMinuteLimit); i++ {
		allowed, err := checkProfileWriteRate(context.Background(), rdb, 42, now)
		if err != nil || !allowed {
			t.Fatalf("request %d = (%v, %v), want allowed", i+1, allowed, err)
		}
	}
	allowed, err := checkProfileWriteRate(context.Background(), rdb, 42, now)
	if err != nil || allowed {
		t.Fatalf("request above minute quota = (%v, %v), want rejected", allowed, err)
	}
	dayCount, err := rdb.ZCard(context.Background(), "agentcard:rl:profile-write-day:42").Result()
	if err != nil || dayCount != profileWriteMinuteLimit {
		t.Fatalf("rejected minute request consumed daily quota: count=%d err=%v", dayCount, err)
	}
	if mr.Exists("agentcard:rl:profile-write-minute:42") == false ||
		mr.Exists("agentcard:rl:profile-write-day:42") == false {
		t.Fatal("one atomic limiter call must maintain both minute and day counters")
	}

	for i := 0; i < int(profileWriteDailyLimit); i++ {
		mr.FastForward(time.Minute + time.Second)
		allowed, err := checkProfileWriteRate(context.Background(), rdb, 84, now.Add(time.Duration(i)*time.Minute))
		if err != nil || !allowed {
			t.Fatalf("daily request %d = (%v, %v), want allowed", i+1, allowed, err)
		}
	}
	allowed, err = checkProfileWriteRate(context.Background(), rdb, 84, now.Add(time.Duration(profileWriteDailyLimit)*time.Minute))
	if err != nil || allowed {
		t.Fatalf("request above daily quota = (%v, %v), want rejected", allowed, err)
	}
}
