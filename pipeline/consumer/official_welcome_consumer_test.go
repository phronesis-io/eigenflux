package consumer

import (
	"context"
	"testing"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	pmdal "eigenflux_server/rpc/pm/dal"

	"gorm.io/gorm/logger"
)

// TestEnsureFriendship covers the deterministic friend-establishment used by the
// welcome flow: a pending request is accepted, the relation is created, and a
// second call is a no-op (no duplicate rows). Does not require the PM service.
func TestEnsureFriendship(t *testing.T) {
	cfg := config.Load()
	db.InitWithLogLevel(cfg.PgDSN, logger.Silent)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

	const officialID int64 = 9_200_000_000_000_000_001
	const userID int64 = 9_200_000_000_000_000_002
	const reqID int64 = 9_200_000_000_000_000_010

	clean := func() {
		db.DB.Exec("DELETE FROM user_relations WHERE from_uid IN (?, ?) OR to_uid IN (?, ?)", officialID, userID, officialID, userID)
		db.DB.Exec("DELETE FROM friend_requests WHERE from_uid IN (?, ?) OR to_uid IN (?, ?)", officialID, userID, officialID, userID)
	}
	clean()
	t.Cleanup(clean)

	c := &OfficialWelcomeConsumer{}

	// A pending request user->official exists (the onboarding apply step).
	if _, err := pmdal.CreateFriendRequest(db.DB, reqID, userID, officialID, "", ""); err != nil {
		t.Fatalf("seed pending request: %v", err)
	}

	if err := c.ensureFriendship(context.Background(), officialID, userID); err != nil {
		t.Fatalf("ensureFriendship: %v", err)
	}

	friend, err := pmdal.IsFriend(db.DB, officialID, userID)
	if err != nil {
		t.Fatalf("IsFriend: %v", err)
	}
	if !friend {
		t.Fatal("expected official and user to be friends")
	}

	req, err := pmdal.GetFriendRequest(db.DB, reqID)
	if err != nil {
		t.Fatalf("GetFriendRequest: %v", err)
	}
	if req.Status != pmdal.RequestStatusAccepted {
		t.Fatalf("request status = %d, want accepted (%d)", req.Status, pmdal.RequestStatusAccepted)
	}

	// Idempotent: a second call must not create duplicate relation rows.
	if err := c.ensureFriendship(context.Background(), officialID, userID); err != nil {
		t.Fatalf("ensureFriendship (second call): %v", err)
	}
	var pairs int64
	db.DB.Raw(
		"SELECT count(*) FROM user_relations WHERE rel_type = ? AND ((from_uid=? AND to_uid=?) OR (from_uid=? AND to_uid=?))",
		pmdal.RelTypeFriend, officialID, userID, userID, officialID,
	).Scan(&pairs)
	if pairs != 2 {
		t.Fatalf("friend relation rows = %d, want 2", pairs)
	}
}
