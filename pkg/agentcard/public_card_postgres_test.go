package agentcard_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"eigenflux_server/pkg/agentcard"
	profiledal "eigenflux_server/rpc/profile/dal"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestEveryPersistedAgentCanBuildAPublicCardOnFirstRead(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL integration semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	now := time.Now().UnixMilli()
	agentID := now*100 + 37
	shortID := "PuBic"
	email := fmt.Sprintf("public-card-%d@example.test", agentID)
	if err := tx.Exec(`INSERT INTO agents
		(agent_id, short_id, email, agent_name, bio, created_at, updated_at)
		VALUES (?, ?, ?, '', '', ?, ?)`, agentID, shortID, email, now, now).Error; err != nil {
		t.Fatal(err)
	}

	// Deliberately do not create agent_profiles, agent_settings, or agent_cards.
	// The anonymous public route uses this same read-on-miss builder, so a
	// persisted Agent is sufficient to obtain its first public Card projection.
	if err := agentcard.RebuildOnMiss(context.Background(), tx, rdb, agentID); err != nil {
		t.Fatalf("read-on-miss public Card build failed: %v", err)
	}
	card, err := profiledal.GetAgentCard(tx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if card.PublicCard == "" || card.SchemaVersion != agentcard.SchemaVersion {
		t.Fatalf("invalid public Card projection: %#v", card)
	}
}
