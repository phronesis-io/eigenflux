package pm

import (
	"context"
	"fmt"
	"testing"

	"eigenflux_server/rpc/pm/icebreak"
	"eigenflux_server/tests/testutil"
)

func cleanIceBreakKeys(t *testing.T, convIDs ...int64) {
	t.Helper()
	ctx := context.Background()
	rdb := testutil.GetTestRedis()
	for _, convID := range convIDs {
		rdb.Del(ctx, fmt.Sprintf("pm:ice:h:%d", convID/1000))
		rdb.Del(ctx, fmt.Sprintf("pm:lock:%d", convID))
	}
}

func TestIceBreak_FirstMessageSetsLock(t *testing.T) {
	rdb := testutil.GetTestRedis()
	ib := icebreak.NewIceBreaker(rdb)
	convID := int64(900001)
	cleanIceBreakKeys(t, convID)
	defer cleanIceBreakKeys(t, convID)

	status, lastSender, err := ib.CheckAndSetIceBreak(context.Background(), convID, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != icebreak.IceStatusFirstMsg {
		t.Fatalf("expected IceStatusFirstMsg(2), got %d", status)
	}
	if lastSender != 100 {
		t.Fatalf("expected lastSender=100, got %d", lastSender)
	}
}

func TestIceBreak_SameSenderReturnsFirstMsg(t *testing.T) {
	rdb := testutil.GetTestRedis()
	ib := icebreak.NewIceBreaker(rdb)
	convID := int64(900002)
	cleanIceBreakKeys(t, convID)
	defer cleanIceBreakKeys(t, convID)

	ctx := context.Background()
	ib.CheckAndSetIceBreak(ctx, convID, 100)

	// Same sender again
	status, lastSender, err := ib.CheckAndSetIceBreak(ctx, convID, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != icebreak.IceStatusFirstMsg {
		t.Fatalf("expected IceStatusFirstMsg(2), got %d", status)
	}
	if lastSender != 100 {
		t.Fatalf("expected lastSender=100, got %d", lastSender)
	}
}

func TestIceBreak_DifferentSenderBreaksIce(t *testing.T) {
	rdb := testutil.GetTestRedis()
	ib := icebreak.NewIceBreaker(rdb)
	convID := int64(900003)
	cleanIceBreakKeys(t, convID)
	defer cleanIceBreakKeys(t, convID)

	ctx := context.Background()
	ib.CheckAndSetIceBreak(ctx, convID, 100)

	// Different sender
	status, lastSender, err := ib.CheckAndSetIceBreak(ctx, convID, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != icebreak.IceStatusJustBroken {
		t.Fatalf("expected IceStatusJustBroken(1), got %d", status)
	}
	if lastSender != 100 {
		t.Fatalf("expected lastSender=100 (original sender), got %d", lastSender)
	}
}

func TestIceBreak_AfterBrokenBothCanMessage(t *testing.T) {
	rdb := testutil.GetTestRedis()
	ib := icebreak.NewIceBreaker(rdb)
	convID := int64(900004)
	cleanIceBreakKeys(t, convID)
	defer cleanIceBreakKeys(t, convID)

	ctx := context.Background()
	ib.CheckAndSetIceBreak(ctx, convID, 100)
	ib.CheckAndSetIceBreak(ctx, convID, 200) // breaks ice

	// Both sides should get IceStatusBroken
	status1, _, err := ib.CheckAndSetIceBreak(ctx, convID, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status1 != icebreak.IceStatusBroken {
		t.Fatalf("expected IceStatusBroken(0) for sender 100, got %d", status1)
	}

	status2, _, err := ib.CheckAndSetIceBreak(ctx, convID, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status2 != icebreak.IceStatusBroken {
		t.Fatalf("expected IceStatusBroken(0) for sender 200, got %d", status2)
	}
}

func TestIceBreak_RollbackFirstMsg(t *testing.T) {
	rdb := testutil.GetTestRedis()
	ib := icebreak.NewIceBreaker(rdb)
	convID := int64(900005)
	cleanIceBreakKeys(t, convID)
	defer cleanIceBreakKeys(t, convID)

	ctx := context.Background()
	status, _, _ := ib.CheckAndSetIceBreak(ctx, convID, 100)
	if status != icebreak.IceStatusFirstMsg {
		t.Fatalf("expected IceStatusFirstMsg, got %d", status)
	}

	// Rollback
	if err := ib.RollbackIceBreak(ctx, convID, icebreak.IceStatusFirstMsg); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	// After rollback, should behave as fresh — first message again
	status2, _, _ := ib.CheckAndSetIceBreak(ctx, convID, 200)
	if status2 != icebreak.IceStatusFirstMsg {
		t.Fatalf("expected IceStatusFirstMsg after rollback, got %d", status2)
	}
}

func TestIceBreak_RollbackJustBroken(t *testing.T) {
	rdb := testutil.GetTestRedis()
	ib := icebreak.NewIceBreaker(rdb)
	convID := int64(900006)
	cleanIceBreakKeys(t, convID)
	defer cleanIceBreakKeys(t, convID)

	ctx := context.Background()
	ib.CheckAndSetIceBreak(ctx, convID, 100)
	status, _, _ := ib.CheckAndSetIceBreak(ctx, convID, 200)
	if status != icebreak.IceStatusJustBroken {
		t.Fatalf("expected IceStatusJustBroken, got %d", status)
	}

	// Rollback the ice break
	if err := ib.RollbackIceBreak(ctx, convID, icebreak.IceStatusJustBroken); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	// After rollback, ice is no longer broken — next call from sender 200 should break it again
	status2, _, _ := ib.CheckAndSetIceBreak(ctx, convID, 200)
	if status2 == icebreak.IceStatusBroken {
		t.Fatalf("expected ice NOT broken after rollback, got IceStatusBroken")
	}
}
