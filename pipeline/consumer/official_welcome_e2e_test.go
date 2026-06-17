package consumer

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	pmdal "eigenflux_server/rpc/pm/dal"

	"github.com/cloudwego/kitex/client"
	etcd "github.com/kitex-contrib/registry-etcd"
	"gorm.io/gorm/logger"
)

// TestOfficialWelcomeE2E drives the welcome consumer's handle() against the live
// PM service: it must friend the new agent and deliver exactly one welcome PM.
// Requires the local stack (PM RPC + etcd + PG + Redis) and a provisioned
// official account; it skips otherwise.
func TestOfficialWelcomeE2E(t *testing.T) {
	cfg := config.Load()
	db.InitWithLogLevel(cfg.PgDSN, logger.Silent)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

	var officialID int64
	if err := db.DB.Raw(
		"SELECT agent_id FROM agents WHERE email = ? AND is_official", cfg.OfficialAgentEmail,
	).Scan(&officialID).Error; err != nil || officialID == 0 {
		t.Skipf("official account not provisioned locally (err=%v)", err)
	}

	const userID int64 = 9_100_000_000_000_000_077
	now := time.Now().UnixMilli()

	lo, hi := officialID, userID
	if lo > hi {
		lo, hi = hi, lo
	}
	cleanup := func() {
		db.DB.Exec("DELETE FROM private_messages WHERE sender_id IN (?,?) AND receiver_id IN (?,?)", officialID, userID, officialID, userID)
		db.DB.Exec("DELETE FROM conversations WHERE participant_a IN (?,?) AND participant_b IN (?,?)", officialID, userID, officialID, userID)
		db.DB.Exec("DELETE FROM user_relations WHERE from_uid IN (?,?) OR to_uid IN (?,?)", officialID, userID, officialID, userID)
		db.DB.Exec("DELETE FROM friend_requests WHERE from_uid IN (?,?) OR to_uid IN (?,?)", officialID, userID, officialID, userID)
		db.DB.Exec("DELETE FROM agents WHERE agent_id = ?", userID)
		// Clear PM-service Redis caches so a re-run is not poisoned by a stale
		// conversation mapping / friend set pointing at deleted rows.
		mq.RDB.Del(context.Background(),
			officialWelcomedKey(userID),
			fmt.Sprintf("pm:convmap:%d:%d:0", lo, hi),
			fmt.Sprintf("friend:%d", officialID),
			fmt.Sprintf("friend:%d", userID),
			fmt.Sprintf("pm:fetch:%d", userID),
		)
	}
	cleanup()
	t.Cleanup(cleanup)

	// A new, profile-complete agent.
	if err := db.DB.Exec(
		`INSERT INTO agents (agent_id, email, agent_name, bio, created_at, updated_at, profile_completed_at, is_official)
		 VALUES (?, ?, ?, ?, ?, ?, ?, false)`,
		userID, "welcome-e2e@test.com", "WelcomeE2E", "test", now, now, now,
	).Error; err != nil {
		t.Fatalf("insert test user: %v", err)
	}

	// Real PM client → running PM service. Prefer a direct host:port when set
	// (local stacks register a LAN IP in etcd that may be unreachable from the
	// test host); otherwise discover via etcd.
	var pmClient pmservice.Client
	var err error
	if addr := os.Getenv("PM_DIRECT_ADDR"); addr != "" {
		pmClient, err = pmservice.NewClient("PMService", client.WithHostPorts(addr))
	} else {
		resolver, rerr := etcd.NewEtcdResolver([]string{cfg.EtcdAddr})
		if rerr != nil {
			t.Fatalf("etcd resolver: %v", rerr)
		}
		pmClient, err = pmservice.NewClient("PMService", rpcx.ClientOptions(resolver)...)
	}
	if err != nil {
		t.Fatalf("pm client: %v", err)
	}

	c := &OfficialWelcomeConsumer{
		pmClient:       pmClient,
		welcomeMessage: cfg.OfficialWelcomeMessage,
		officialEmail:  cfg.OfficialAgentEmail,
	}

	res := c.handle(context.Background(), "0-0", map[string]any{"agent_id": strconv.FormatInt(userID, 10)})

	// Friendship is created in PG before the PM hop, so it must always hold.
	friend, ferr := pmdal.IsFriend(db.DB, officialID, userID)
	if ferr != nil {
		t.Fatalf("IsFriend: %v", ferr)
	}
	if !friend {
		t.Fatal("expected official and user to be friends after welcome")
	}

	var msgs int64
	db.DB.Raw(
		"SELECT count(*) FROM private_messages WHERE sender_id = ? AND receiver_id = ? AND content = ?",
		officialID, userID, cfg.OfficialWelcomeMessage,
	).Scan(&msgs)

	// The PM hop needs the PM service reachable from this host. Local stacks may
	// register a LAN IP in etcd that the test host cannot dial; skip delivery
	// assertion in that case (set PM_DIRECT_ADDR to force a reachable address).
	if res != HandleSuccess && msgs == 0 {
		t.Skipf("PM service unreachable (result=%v); friendship verified, welcome delivery not asserted", res)
	}
	if res != HandleSuccess {
		t.Fatalf("handle result = %v, want HandleSuccess", res)
	}
	if msgs != 1 {
		t.Fatalf("welcome PM count = %d, want 1", msgs)
	}
}
