package dal

import (
	"database/sql"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
)

func TestConsoleRawItemProjectionUsesOnlyRequiredColumns(t *testing.T) {
	want := "r.item_id, r.author_agent_id, r.raw_content"
	if consoleRawItemProjection != want {
		t.Fatalf("projection=%q, want %q", consoleRawItemProjection, want)
	}
}

func TestBatchGetRawItemInfo(t *testing.T) {
	cfg := config.Load()

	// Probe connectivity without calling db.Init (which calls os.Exit on failure).
	probe, err := sql.Open("pgx", cfg.PgDSN)
	if err != nil {
		t.Skipf("db not available: %v", err)
	}
	if err := probe.Ping(); err != nil {
		probe.Close()
		t.Skipf("db not available: %v", err)
	}
	probe.Close()

	db.Init(cfg.PgDSN)

	authorID := int64(999900001)
	withURLID := int64(999900100)
	withoutURLID := int64(999900101)
	pendingID := int64(999900102)

	db.DB.Exec("DELETE FROM processed_items WHERE item_id IN (?, ?, ?)", withURLID, withoutURLID, pendingID)
	db.DB.Exec("DELETE FROM raw_items WHERE item_id IN (?, ?, ?)", withURLID, withoutURLID, pendingID)
	db.DB.Exec("DELETE FROM agents WHERE agent_id = ?", authorID)
	t.Cleanup(func() {
		db.DB.Exec("DELETE FROM processed_items WHERE item_id IN (?, ?, ?)", withURLID, withoutURLID, pendingID)
		db.DB.Exec("DELETE FROM raw_items WHERE item_id IN (?, ?, ?)", withURLID, withoutURLID, pendingID)
		db.DB.Exec("DELETE FROM agents WHERE agent_id = ?", authorID)
	})
	if err := db.DB.Exec(
		"INSERT INTO agents (agent_id, short_id, email, agent_name, created_at, updated_at) VALUES (?, 'RaWin', 'raw-info@example.com', 'Raw Info', extract(epoch from now())::bigint, extract(epoch from now())::bigint)",
		authorID,
	).Error; err != nil {
		t.Fatalf("insert author: %v", err)
	}

	longContent := strings.Repeat("界", 1200)
	if err := db.DB.Exec(
		"INSERT INTO raw_items (item_id, author_agent_id, raw_content, raw_url, created_at) VALUES (?, ?, ?, 'https://ex.test/a', extract(epoch from now())::bigint)",
		withURLID, authorID, longContent,
	).Error; err != nil {
		t.Fatalf("insert with url: %v", err)
	}
	if err := db.DB.Exec(
		"INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES (?, ?, 'x', extract(epoch from now())::bigint)",
		withoutURLID, authorID,
	).Error; err != nil {
		t.Fatalf("insert without url: %v", err)
	}
	if err := db.DB.Exec(
		"INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES (?, ?, 'pending', extract(epoch from now())::bigint)",
		pendingID, authorID,
	).Error; err != nil {
		t.Fatalf("insert pending raw item: %v", err)
	}
	for _, row := range []struct {
		itemID int64
		status int16
	}{{withURLID, StatusCompleted}, {withoutURLID, StatusCompleted}, {pendingID, StatusPending}} {
		if err := db.DB.Exec(
			"INSERT INTO processed_items (item_id, status, updated_at) VALUES (?, ?, extract(epoch from now())::bigint)",
			row.itemID, row.status,
		).Error; err != nil {
			t.Fatalf("insert processed item %d: %v", row.itemID, err)
		}
	}

	got, err := BatchGetRawItemInfo(db.DB, []int64{withURLID, withoutURLID, pendingID})
	if err != nil {
		t.Fatalf("BatchGetRawItemInfo: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[withURLID].AuthorAgentID != authorID || got[withURLID].RawURL != "https://ex.test/a" {
		t.Errorf("with-url row wrong: %+v", got[withURLID])
	}
	if len([]rune(got[withURLID].RawContent)) != 1001 || got[withURLID].AuthorEmail != "raw-info@example.com" || !got[withURLID].AuthorExists || got[withURLID].IsOfficial {
		t.Errorf("with-url enrichment wrong: %+v", got[withURLID])
	}
	if got[withoutURLID].AuthorAgentID != authorID || got[withoutURLID].RawURL != "" {
		t.Errorf("without-url row wrong: %+v", got[withoutURLID])
	}
	if _, exists := got[pendingID]; exists {
		t.Errorf("pending item must be omitted: %+v", got[pendingID])
	}

	completed, err := BatchGetCompletedRawItemsByID(db.DB, []int64{withURLID, pendingID})
	if err != nil {
		t.Fatalf("BatchGetCompletedRawItemsByID: %v", err)
	}
	if completed[withURLID].RawContent != longContent {
		t.Errorf("completed console lookup must return full raw content")
	}
	if _, exists := completed[pendingID]; exists {
		t.Errorf("completed console lookup must omit pending item")
	}

	owned, err := BatchGetRawItemsByID(db.DB, []int64{withURLID, pendingID})
	if err != nil {
		t.Fatalf("BatchGetRawItemsByID: %v", err)
	}
	if owned[withURLID].RawContent != longContent || owned[pendingID].RawContent != "pending" {
		t.Errorf("owner console lookup must preserve full raw content: %+v", owned)
	}
}

func TestGetRecentItemInteractions(t *testing.T) {
	cfg := config.Load()

	// Probe connectivity without calling db.Init (which calls os.Exit on failure).
	probe, err := sql.Open("pgx", cfg.PgDSN)
	if err != nil {
		t.Skipf("db not available: %v", err)
	}
	if err := probe.Ping(); err != nil {
		probe.Close()
		t.Skipf("db not available: %v", err)
	}
	probe.Close()

	db.Init(cfg.PgDSN)

	const itemID = int64(999900200)
	namedAgent := int64(999900210) // has a row in agents → name resolves
	ghostAgent := int64(999900211) // no agents row → LEFT JOIN yields empty name

	db.DB.Exec("DELETE FROM feedback_logs WHERE item_id = ?", itemID)
	db.DB.Exec("DELETE FROM agents WHERE agent_id = ?", namedAgent)
	t.Cleanup(func() {
		db.DB.Exec("DELETE FROM feedback_logs WHERE item_id = ?", itemID)
		db.DB.Exec("DELETE FROM agents WHERE agent_id = ?", namedAgent)
	})

	if err := db.DB.Exec(
		"INSERT INTO agents (agent_id, short_id, email, agent_name, created_at, updated_at) VALUES (?, 'ScOut', ?, 'Scout', extract(epoch from now())::bigint, extract(epoch from now())::bigint)",
		namedAgent, "scout-999900210@ex.test",
	).Error; err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	// Three feedback rows; feedback_at ascending so we can assert newest-first ordering.
	rows := []struct {
		msgID      string
		agentID    int64
		score      int16
		feedbackAt int64
	}{
		{"itx-1", namedAgent, 2, 1000},
		{"itx-2", ghostAgent, -1, 2000},
		{"itx-3", namedAgent, 1, 3000},
	}
	for _, r := range rows {
		if err := db.DB.Exec(
			"INSERT INTO feedback_logs (stream_message_id, impression_id, agent_id, item_id, score, feedback_at, created_at) VALUES (?, '', ?, ?, ?, ?, ?)",
			r.msgID, r.agentID, itemID, r.score, r.feedbackAt, r.feedbackAt,
		).Error; err != nil {
			t.Fatalf("insert feedback %s: %v", r.msgID, err)
		}
	}

	callerAgent := int64(999900299)
	got, err := GetRecentItemInteractions(db.DB, itemID, callerAgent, 15)
	if err != nil {
		t.Fatalf("GetRecentItemInteractions: %v", err)
	}
	// Only "found helpful" feedback (score >= 1) is returned; the -1 ghost row is
	// filtered at the interface layer.
	if len(got) != 2 {
		t.Fatalf("want 2 helpful interactions, got %d", len(got))
	}
	// Newest first.
	if got[0].FeedbackAt != 3000 || got[1].FeedbackAt != 1000 {
		t.Errorf("not ordered newest-first: %+v", got)
	}
	// Name joins for the known agent; no friend relation set up, so is_friend is false.
	if got[0].AgentID != namedAgent || got[0].AgentName != "Scout" || got[0].Score != 1 {
		t.Errorf("row0 wrong: %+v", got[0])
	}
	if got[1].AgentID != namedAgent || got[1].Score != 2 {
		t.Errorf("row1 wrong: %+v", got[1])
	}
	if got[0].IsFriend {
		t.Errorf("expected is_friend=false with no relation, got %+v", got[0])
	}

	// limit caps the result set.
	limited, err := GetRecentItemInteractions(db.DB, itemID, callerAgent, 1)
	if err != nil {
		t.Fatalf("GetRecentItemInteractions limited: %v", err)
	}
	if len(limited) != 1 || limited[0].FeedbackAt != 3000 {
		t.Errorf("limit not applied: %+v", limited)
	}
}
