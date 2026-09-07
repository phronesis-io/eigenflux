package icebreak

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestInitiatorLimitReturnsRetryAfter(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	breaker := NewIceBreaker(client)
	ctx := context.Background()
	const convID int64 = 123456

	for attempt := 1; attempt <= MaxInitiatorMsgs; attempt++ {
		status, _, err := breaker.CheckAndSetIceBreak(ctx, convID, 42)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if status != IceStatusFirstMsg {
			t.Fatalf("attempt %d status=%d, want %d", attempt, status, IceStatusFirstMsg)
		}
	}

	status, _, err := breaker.CheckAndSetIceBreak(ctx, convID, 42)
	if err != nil {
		t.Fatal(err)
	}
	if status != IceStatusLimitReached {
		t.Fatalf("fourth attempt status=%d, want %d", status, IceStatusLimitReached)
	}

	server.FastForward(1500 * time.Millisecond)
	retryAfter, err := breaker.RetryAfterSeconds(ctx, convID)
	if err != nil {
		t.Fatal(err)
	}
	if retryAfter != 86399 {
		t.Fatalf("retryAfter=%d, want 86399", retryAfter)
	}
}
