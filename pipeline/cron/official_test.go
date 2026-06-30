package main

import (
	"strings"
	"testing"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"

	"gorm.io/gorm/logger"
)

// TestPickTrendingTopics: top-poolN by frequency, then ≤pickN sampled.
func TestPickTrendingTopics(t *testing.T) {
	counts := map[string]int64{"ai": 100, "fintech": 80, "devops": 60, "nlp": 40, "rust": 20, "": 999}
	got := pickTrendingTopics(counts, 4, 3)
	if len(got) != 3 {
		t.Fatalf("pick count = %d, want 3", len(got))
	}
	for _, g := range got {
		if g == "" {
			t.Fatal("empty tag must be filtered out")
		}
		// Must come from the top-4 pool (ai/fintech/devops/nlp), never rust (5th).
		if g == "rust" {
			t.Fatalf("rust is outside the top-%d pool, should not be picked", 4)
		}
	}
}

// TestOfficialPromptRenders: official.tmpl loads and renders with a Task.
func TestOfficialPromptRenders(t *testing.T) {
	prompts, err := llm.LoadDefaultPrompts()
	if err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	out, err := prompts.Render("official", map[string]any{"Task": "UNIQUE_TASK_MARKER"})
	if err != nil {
		t.Fatalf("render official: %v", err)
	}
	if !strings.Contains(out, "EigenFlux") || !strings.Contains(out, "UNIQUE_TASK_MARKER") {
		t.Fatalf("rendered prompt missing role text or task: %q", out)
	}
}

// TestOfficialFeedRescueQueries inserts a synthetic replay_logs row and verifies
// the two helpers — especially the jsonb_exists_any domain-overlap query with a
// Go []string bound to text[] — against real Postgres.
func TestOfficialFeedRescueQueries(t *testing.T) {
	cfg := config.Load()
	db.InitWithLogLevel(cfg.PgDSN, logger.Silent)

	const agentID int64 = 9_100_000_000_000_000_201
	const itemID int64 = 9_100_000_000_000_000_202
	const impr = "test-official-rescue-1"
	clean := func() { db.DB.Exec("DELETE FROM replay_logs WHERE agent_id = ?", agentID) }
	clean()
	t.Cleanup(clean)

	if err := db.DB.Exec(
		`INSERT INTO replay_logs (id, impression_id, agent_id, item_id, agent_features, item_features, served_at, created_at, position, delivered)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, true)`,
		agentID, impr, agentID, itemID,
		`{"domains":["tech","ai"]}`, `{"domains":["ai"]}`,
		int64(1), int64(1),
	).Error; err != nil {
		t.Fatalf("insert synthetic replay row: %v", err)
	}

	if got := agentRecentDomains(agentID); len(got) != 2 || got[0] != "tech" {
		t.Fatalf("agentRecentDomains = %v, want [tech ai]", got)
	}

	// Overlap on "ai" → counts the item.
	n, err := deliveredCountInDomains(agentID, 0, []string{"ai"})
	if err != nil {
		t.Fatalf("deliveredCountInDomains (jsonb_exists_any) failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("overlap count = %d, want 1", n)
	}
	// No overlap → 0.
	n2, err := deliveredCountInDomains(agentID, 0, []string{"finance"})
	if err != nil {
		t.Fatalf("deliveredCountInDomains (no overlap) failed: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("non-overlap count = %d, want 0", n2)
	}
}
