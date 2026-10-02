package searchguard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type page struct{ IDs []int64 }

func fixture(t *testing.T) (*Guard, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { client.Close() })
	return &Guard{}, client, server
}
func TestCacheIsolationExpiryAndAuthoritativeVisibility(t *testing.T) {
	g, r, m := fixture(t)
	ctx := context.Background()
	calls := 0
	visible := true
	checks := 0
	load := func(context.Context) (any, error) { calls++; return &page{IDs: []int64{1}}, nil }
	read := func(owner int64, key any) page {
		t.Helper()
		out := page{}
		err := g.Load(ctx, r, "records", owner, key, &out, load, func(context.Context) error {
			checks++
			if !visible {
				out.IDs = nil
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	key := struct {
		Query, Status string
		Cursor, Limit int
	}{"合同_%!", "open", 0, 10}
	out := read(1, key)
	out.IDs[0] = 999
	if got := read(1, key); got.IDs[0] != 1 || calls != 1 {
		t.Fatalf("mutable cache or repeated fill: %v %d", got, calls)
	}
	visible = false
	if got := read(1, key); len(got.IDs) != 0 || calls != 1 {
		t.Fatal(got, calls)
	}
	visible = true
	read(2, key)
	for _, changed := range []struct {
		Query, Status string
		Cursor, Limit int
	}{{"合同_!", "open", 0, 10}, {key.Query, "closed", 0, 10}, {key.Query, "open", 1, 10}, {key.Query, "open", 0, 20}} {
		read(1, changed)
	}
	if calls != 6 {
		t.Fatal("cache key omitted request or owner", calls)
	}
	m.FastForward(CacheTTL + time.Millisecond)
	read(1, key)
	if calls != 7 || checks != 9 {
		t.Fatal(calls, checks)
	}
}
func TestSingleflightAndCancelledFirstWaiter(t *testing.T) {
	g, r, _ := fixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	load := func(ctx context.Context) (any, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return &page{IDs: []int64{7}}, nil
	}
	first, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { var out page; firstDone <- g.Load(first, r, "records", 1, "same", &out, load, nil) }()
	<-entered
	const waiters = 12
	var wg sync.WaitGroup
	errs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out page
			err := g.Load(context.Background(), r, "records", 1, "same", &out, load, nil)
			if err == nil && (len(out.IDs) != 1 || out.IDs[0] != 7) {
				err = fmt.Errorf("bad response: %v", out)
			}
			errs <- err
		}()
	}
	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("fills=%d", calls.Load())
	}
}
func TestErrorsAreNotCachedAndRedisFailureDoesNotHitDB(t *testing.T) {
	g, r, m := fixture(t)
	calls := 0
	sentinel := errors.New("database down")
	load := func(context.Context) (any, error) { calls++; return nil, sentinel }
	for i := 0; i < 2; i++ {
		var out page
		if err := g.Load(context.Background(), r, "records", 1, "x", &out, load, nil); !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	m.Close()
	var out page
	if err := g.Load(context.Background(), r, "records", 1, "x", &out, load, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("Redis outage hit database")
	}
	if err := g.Load(context.Background(), nil, "records", 1, "x", &out, load, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestOwnerBudgetSharedAcrossInstances(t *testing.T) {
	g, r, m := fixture(t)
	other := &Guard{}
	calls := 0
	load := func(context.Context) (any, error) { calls++; return &page{}, nil }
	for i := 0; i < OwnerLimit; i++ {
		var out page
		if err := g.Load(context.Background(), r, "records", 1, "x", &out, load, nil); err != nil {
			t.Fatal(err)
		}
	}
	var out page
	if err := other.Load(context.Background(), r, "records", 1, "x", &out, load, nil); !errors.Is(err, ErrLimited) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	if err := other.Load(context.Background(), r, "records", 2, "x", &out, load, nil); err != nil {
		t.Fatal(err)
	}
	m.FastForward(RateWindow + time.Millisecond)
	if err := g.Load(context.Background(), r, "records", 1, "x", &out, load, nil); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentDistinctQueriesHaveBoundedDBWork(t *testing.T) {
	g, r, _ := fixture(t)
	entered := make(chan struct{}, MaxConcurrent)
	release := make(chan struct{})
	results := make(chan error, MaxConcurrent)
	load := func(context.Context) (any, error) { entered <- struct{}{}; <-release; return &page{}, nil }
	for i := 0; i < MaxConcurrent; i++ {
		go func(i int) {
			var out page
			results <- g.Load(context.Background(), r, "records", int64(i+1), i, &out, load, nil)
		}(i)
	}
	for i := 0; i < MaxConcurrent; i++ {
		<-entered
	}
	var out page
	if err := g.Load(context.Background(), r, "records", 99, "overflow", &out, load, nil); !errors.Is(err, ErrLimited) {
		t.Fatal(err)
	}
	close(release)
	for i := 0; i < MaxConcurrent; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestGlobalFillBudgetLimitsDistinctOwnersAcrossInstances(t *testing.T) {
	g, r, _ := fixture(t)
	other := &Guard{}
	calls := 0
	load := func(context.Context) (any, error) { calls++; return &page{}, nil }
	for i := 0; i < 100; i++ {
		var out page
		if err := g.Load(context.Background(), r, "records", int64(i+1), "x", &out, load, nil); err != nil {
			t.Fatal(i, err)
		}
	}
	var out page
	if err := other.Load(context.Background(), r, "records", 101, "x", &out, load, nil); !errors.Is(err, ErrLimited) {
		t.Fatal(err)
	}
	if calls != 100 {
		t.Fatal(calls)
	}
}
