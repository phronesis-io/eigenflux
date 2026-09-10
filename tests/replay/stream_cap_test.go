package replay_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/mq"
	"github.com/redis/go-redis/v9"
)

// TestStreamCap verifies mq.Publish bounds ordinary streams via approximate
// MAXLEN trimming, leaves ingestion streams unbounded (exempt), and that
// PublishCapped honours an explicit cap. This guards the fix for
// stream:replay:log growing without limit because XACK never removes entries
// from a stream.
func TestStreamCap(t *testing.T) {
	cfg := config.Load()
	// The exemption must use the production key name, so use a separate Redis
	// database. Deleting that key in DB 0 would destroy the live pipeline group.
	testDB := 15
	if raw := os.Getenv("EIGENFLUX_TEST_REDIS_DB"); raw != "" {
		var err error
		testDB, err = strconv.Atoi(raw)
		if err != nil || testDB <= 0 {
			t.Fatal("EIGENFLUX_TEST_REDIS_DB must be a positive isolated Redis database number")
		}
	}
	previous := mq.RDB
	mq.RDB = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: testDB})
	t.Cleanup(func() {
		_ = mq.RDB.Close()
		mq.RDB = previous
	})
	mq.SetDefaultStreamMaxLen(20000)

	ctx := context.Background()
	publishN := func(stream string, n int, capped func(string, int) error) {
		if exists, err := mq.RDB.Exists(ctx, stream).Result(); err != nil || exists != 0 {
			t.Fatalf("isolated Redis DB %d key %s must be unused: exists=%d err=%v", testDB, stream, exists, err)
		}
		t.Cleanup(func() { mq.RDB.Del(ctx, stream) })
		for i := range n {
			if err := capped(stream, i); err != nil {
				t.Fatalf("publish %s seq=%d: %v", stream, i, err)
			}
		}
	}
	xlen := func(stream string) int64 {
		n, err := mq.RDB.XLen(ctx, stream).Result()
		if err != nil {
			t.Fatalf("XLen %s: %v", stream, err)
		}
		return n
	}

	t.Run("default cap trims ordinary stream", func(t *testing.T) {
		mq.SetDefaultStreamMaxLen(200)
		t.Cleanup(func() { mq.SetDefaultStreamMaxLen(20000) })

		const stream = "test:stream:cap:ordinary"
		const published = 3000
		publishN(stream, published, func(s string, i int) error {
			_, err := mq.Publish(ctx, s, map[string]interface{}{"seq": strconv.Itoa(i)})
			return err
		})

		got := xlen(stream)
		// Approx trimming works on whole macro-nodes, so XLEN may exceed the cap
		// by up to a node's worth, but must be far below the published count.
		if got >= published {
			t.Fatalf("stream not trimmed: xlen=%d, published=%d", got, published)
		}
		if got > 200+1000 {
			t.Fatalf("stream trimmed too loosely: xlen=%d", got)
		}
	})

	t.Run("ingestion stream is exempt and unbounded", func(t *testing.T) {
		mq.SetDefaultStreamMaxLen(200)
		t.Cleanup(func() { mq.SetDefaultStreamMaxLen(20000) })

		const stream = "stream:item:publish"
		const published = 500
		publishN(stream, published, func(s string, i int) error {
			_, err := mq.Publish(ctx, s, map[string]interface{}{"seq": strconv.Itoa(i)})
			return err
		})

		if got := xlen(stream); got != published {
			t.Fatalf("exempt stream trimmed: xlen=%d, want %d", got, published)
		}
	})

	t.Run("PublishCapped honours explicit unbounded", func(t *testing.T) {
		const stream = "test:stream:cap:explicit"
		const published = 300
		publishN(stream, published, func(s string, i int) error {
			_, err := mq.PublishCapped(ctx, s, 0, map[string]interface{}{"seq": strconv.Itoa(i)})
			return err
		})

		if got := xlen(stream); got != published {
			t.Fatalf("explicit unbounded trimmed: xlen=%d, want %d", got, published)
		}
	})
}
