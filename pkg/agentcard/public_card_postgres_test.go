package agentcard_test

import (
	"context"
	"encoding/json"
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
		(agent_id, short_id, email, agent_name, agent_name_en, bio, created_at, updated_at)
		VALUES (?, ?, ?, '星图研究助手', 'Atlas Research Assistant', '', ?, ?)`, agentID, shortID, email, now, now).Error; err != nil {
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
	var projection map[string]interface{}
	if err := json.Unmarshal([]byte(card.PublicCard), &projection); err != nil {
		t.Fatal(err)
	}
	if projection["agent_name_en"] != "Atlas Research Assistant" || projection["display_name_en"] != "Atlas Research Assistant" {
		t.Fatalf("public Card English display name missing: %#v", projection)
	}
}

func TestCardTopItemsExposeFiveHighestScoredBroadcasts(t *testing.T) {
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
	agentID := now*100 + 41
	itemBase := agentID * 10
	if err := tx.Exec(`INSERT INTO agents
		(agent_id, short_id, email, agent_name, bio, created_at, updated_at)
		VALUES (?, 'TpFiv', ?, 'top-five', '', ?, ?)`, agentID, fmt.Sprintf("top-five-%d@example.test", agentID), now, now).Error; err != nil {
		t.Fatal(err)
	}

	type fixture struct {
		itemID      int64
		score       int64
		praiseCount int64
		content     string
		publishedAt int64
	}
	fixtures := []fixture{
		{itemID: itemBase + 6, score: 0, praiseCount: 0, content: "sixth zero-score broadcast", publishedAt: now - 6000},
		{itemID: itemBase + 5, score: 0, praiseCount: 0, content: "fifth zero-score broadcast", publishedAt: now - 5000},
		{itemID: itemBase + 4, score: 4, praiseCount: 1, content: "fourth broadcast", publishedAt: now - 4000},
		{itemID: itemBase + 3, score: 12, praiseCount: 5, content: "third broadcast", publishedAt: now - 3000},
		{itemID: itemBase + 2, score: 8, praiseCount: 3, content: "second broadcast", publishedAt: now - 2000},
		{itemID: itemBase + 1, score: 15, praiseCount: 7, content: "first broadcast", publishedAt: now - 1000},
	}
	for _, f := range fixtures {
		if err := tx.Exec(`INSERT INTO raw_items(item_id, author_agent_id, raw_content, created_at)
			VALUES (?, ?, ?, ?)`, f.itemID, agentID, f.content, f.publishedAt).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Exec(`INSERT INTO processed_items(item_id, status, summary, updated_at)
			VALUES (?, ?, ?, ?)`, f.itemID, 3, "summary-"+f.content, f.publishedAt).Error; err != nil {
			t.Fatal(err)
		}
		// Stats can be created later than the broadcast; published_at must remain
		// anchored to raw_items.created_at.
		if err := tx.Exec(`INSERT INTO item_stats(item_id, author_agent_id, score_1_count, total_score, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, f.itemID, agentID, f.praiseCount, f.score, f.publishedAt+500, f.publishedAt+500).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := agentcard.Rebuild(context.Background(), tx, rdb, agentID); err != nil {
		t.Fatalf("rebuild card: %v", err)
	}
	card, err := profiledal.GetAgentCard(tx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	var projection struct {
		Influence struct {
			TopItems []struct {
				ItemID      string `json:"item_id"`
				Score       int64  `json:"score"`
				Summary     string `json:"summary"`
				Content     string `json:"content"`
				PraiseCount int64  `json:"praise_count"`
				PublishedAt int64  `json:"published_at"`
			} `json:"top_items"`
		} `json:"influence"`
	}
	if err := json.Unmarshal([]byte(card.PublicCard), &projection); err != nil {
		t.Fatal(err)
	}
	got := projection.Influence.TopItems
	if len(got) != 5 {
		t.Fatalf("top_items count=%d, want 5", len(got))
	}
	// The fifth entry proves completed zero-score broadcasts fill the list when
	// fewer than five positive-score broadcasts exist. item_id breaks the tie.
	want := []fixture{fixtures[5], fixtures[3], fixtures[4], fixtures[2], fixtures[1]}
	for i, f := range want {
		if got[i].ItemID != fmt.Sprint(f.itemID) || got[i].Score != f.score || got[i].Content != f.content || got[i].PraiseCount != f.praiseCount || got[i].PublishedAt != f.publishedAt {
			t.Fatalf("top_items[%d]=%+v, want item=%d score=%d content=%q praise=%d published=%d", i, got[i], f.itemID, f.score, f.content, f.praiseCount, f.publishedAt)
		}
		if got[i].Summary != "summary-"+f.content {
			t.Fatalf("top_items[%d] summary=%q, want backward-compatible summary", i, got[i].Summary)
		}
	}
}
