package sendguard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestGuard(t *testing.T) (*Guard, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return New(client), server
}

func TestGuardReplaysCompletedSend(t *testing.T) {
	guard, _ := newTestGuard(t)
	ctx := context.Background()
	key := Fingerprint(1, "item", 2, "完全相同")

	lease, replay, err := guard.Acquire(ctx, key)
	if err != nil || lease == nil || replay != nil {
		t.Fatalf("first acquire = lease %v replay %v err %v", lease, replay, err)
	}
	if err := guard.Complete(ctx, lease, Result{MsgID: 11, ConvID: 22}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	lease, replay, err = guard.Acquire(ctx, key)
	if err != nil || lease != nil || replay == nil {
		t.Fatalf("second acquire = lease %v replay %v err %v", lease, replay, err)
	}
	if replay.MsgID != 11 || replay.ConvID != 22 {
		t.Fatalf("replayed result = %+v", replay)
	}
}

func TestGuardRejectsConcurrentIdenticalSend(t *testing.T) {
	guard, _ := newTestGuard(t)
	ctx := context.Background()
	key := Fingerprint(1, "conversation", 2, "same")

	lease, _, err := guard.Acquire(ctx, key)
	if err != nil || lease == nil {
		t.Fatalf("first acquire: lease %v err %v", lease, err)
	}
	if _, _, err := guard.Acquire(ctx, key); !errors.Is(err, ErrInProgress) {
		t.Fatalf("second acquire error = %v, want %v", err, ErrInProgress)
	}
	if err := guard.Release(ctx, lease); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if lease, _, err := guard.Acquire(ctx, key); err != nil || lease == nil {
		t.Fatalf("acquire after release: lease %v err %v", lease, err)
	}
}

func TestGuardAllowsSendAfterDuplicateWindow(t *testing.T) {
	guard, server := newTestGuard(t)
	ctx := context.Background()
	key := Fingerprint(1, "friend", 2, "same")
	lease, _, err := guard.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := guard.Complete(ctx, lease, Result{MsgID: 11, ConvID: 22}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	server.FastForward(time.Minute + time.Second)
	if lease, replay, err := guard.Acquire(ctx, key); err != nil || lease == nil || replay != nil {
		t.Fatalf("acquire after window = lease %v replay %v err %v", lease, replay, err)
	}
}

func TestFingerprintSeparatesTargetsAndSenders(t *testing.T) {
	base := Fingerprint(1, "item", 2, "same")
	for _, other := range []string{
		Fingerprint(2, "item", 2, "same"),
		Fingerprint(1, "item", 3, "same"),
		Fingerprint(1, "conversation", 2, "same"),
		Fingerprint(1, "item", 2, "different"),
	} {
		if other == base {
			t.Fatal("fingerprint collision between distinct sends")
		}
	}
}
