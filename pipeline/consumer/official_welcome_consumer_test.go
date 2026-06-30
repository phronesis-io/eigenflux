package consumer

import (
	"context"
	"strconv"
	"testing"
	"time"

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

// TestWelcomeWhitelistSkips verifies that with a non-empty whitelist, a
// profile-complete agent whose email is not listed is skipped — no friendship,
// no welcome. Isolates the rollout gate (returns before the PM hop).
func TestWelcomeWhitelistSkips(t *testing.T) {
	cfg := config.Load()
	db.InitWithLogLevel(cfg.PgDSN, logger.Silent)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

	var officialID int64
	if err := db.DB.Raw(
		"SELECT agent_id FROM agents WHERE email = ? AND is_official", cfg.OfficialAgentEmail,
	).Scan(&officialID).Error; err != nil || officialID == 0 {
		t.Skipf("official account not provisioned locally (err=%v)", err)
	}

	const userID int64 = 9_200_000_000_000_000_055
	now := time.Now().UnixMilli()
	clean := func() {
		db.DB.Exec("DELETE FROM user_relations WHERE from_uid IN (?,?) OR to_uid IN (?,?)", officialID, userID, officialID, userID)
		db.DB.Exec("DELETE FROM agents WHERE agent_id = ?", userID)
		mq.RDB.Del(context.Background(), officialWelcomedKey(userID))
	}
	clean()
	t.Cleanup(clean)

	if err := db.DB.Exec(
		`INSERT INTO agents (agent_id, email, agent_name, bio, created_at, updated_at, profile_completed_at, is_official)
		 VALUES (?, ?, ?, ?, ?, ?, ?, false)`,
		userID, "not-listed@test.com", "NotListed", "x", now, now, now,
	).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}

	c := &OfficialWelcomeConsumer{
		officialEmail: cfg.OfficialAgentEmail,
		whitelist:     map[string]struct{}{"someone-else@test.com": {}},
	}

	res := c.handle(context.Background(), "0-0", map[string]any{"agent_id": strconv.FormatInt(userID, 10)})
	if res != HandleSuccess {
		t.Fatalf("handle result = %v, want HandleSuccess (skip)", res)
	}
	if friend, _ := pmdal.IsFriend(db.DB, officialID, userID); friend {
		t.Fatal("non-whitelisted agent must not be friended")
	}
	if n, _ := mq.RDB.Exists(context.Background(), officialWelcomedKey(userID)).Result(); n != 0 {
		t.Fatal("non-whitelisted agent must not consume the welcome gate")
	}
}
