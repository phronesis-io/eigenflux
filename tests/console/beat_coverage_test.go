package console_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

// ---------- Auth Guard ----------

func TestBeatCoverageRequiresAuth(t *testing.T) {
	testutil.WaitForAPI(t)

	req, _ := http.NewRequest("GET", testutil.BaseURL+"/api/v1/agents/me/beat_coverage", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

// ---------- Empty keywords ----------

func TestBeatCoverageEmptyKeywordsReturnsEmptyBeats(t *testing.T) {
	testutil.WaitForAPI(t)
	// Fresh agent: no profile keywords, so no beats.
	email := fmt.Sprintf("beat-empty-%d@test.com", time.Now().UnixNano()%1_000_000)
	token, _, _ := testutil.LoginAndGetToken(t, email)

	result := testutil.DoGet(t, "/api/v1/agents/me/beat_coverage", token)
	assertCode(t, result, 0)

	data := result["data"].(map[string]interface{})
	if data["window"] != "7d" {
		t.Fatalf("expected default window=7d, got %v", data["window"])
	}
	requireKey(t, data, "total_scanned")
	beats, ok := data["beats"].([]interface{})
	if !ok {
		t.Fatalf("expected beats array, got %T", data["beats"])
	}
	if len(beats) != 0 {
		t.Fatalf("expected empty beats for agent without keywords, got %d", len(beats))
	}
}

// ---------- Window parsing ----------

func TestBeatCoverageWindowClamp(t *testing.T) {
	testutil.WaitForAPI(t)
	email := fmt.Sprintf("beat-window-%d@test.com", time.Now().UnixNano()%1_000_000)
	token, _, _ := testutil.LoginAndGetToken(t, email)

	cases := []struct {
		query string
		want  string
	}{
		{"window=14d", "14d"},
		{"window=99d", "30d"}, // clamped to max
		{"window=0d", "1d"},   // clamped to min
		{"window=abcd", "7d"}, // unparseable -> default
		{"", "7d"},            // absent -> default
	}
	for _, tc := range cases {
		path := "/api/v1/agents/me/beat_coverage"
		if tc.query != "" {
			path += "?" + tc.query
		}
		result := testutil.DoGet(t, path, token)
		assertCode(t, result, 0)
		data := result["data"].(map[string]interface{})
		if data["window"] != tc.want {
			t.Fatalf("query %q: expected window=%s, got %v", tc.query, tc.want, data["window"])
		}
	}
}

// ---------- Shape and seeded counts ----------

func TestBeatCoverageSeededCounts(t *testing.T) {
	testutil.WaitForAPI(t)
	nano := time.Now().UnixNano()
	email := fmt.Sprintf("beat-counts-%d@test.com", nano%1_000_000)
	token, agentID, _ := testutil.LoginAndGetToken(t, email)

	// Unique beat keywords so concurrent network data can't pollute the counts.
	kwA := fmt.Sprintf("beatkw-a-%d", nano)
	kwB := fmt.Sprintf("beatkw-b-%d", nano)

	// Seed directly instead of going through feed poll: recall is not
	// deterministic and the delivery insert is async.
	nowMs := time.Now().UnixMilli()
	if _, err := testutil.TestDB.Exec(`
		INSERT INTO agent_profiles (agent_id, status, keywords, updated_at)
		VALUES ($1, 3, $2, $3)
		ON CONFLICT (agent_id) DO UPDATE SET keywords = $2, status = 3, updated_at = $3`,
		agentID, kwA+","+kwB, nowMs,
	); err != nil {
		t.Fatalf("failed to seed agent_profiles: %v", err)
	}

	// item1/item2 -> kwA (item2 via uppercase + space-separated form of the
	// hyphenated beat, verifying case- and separator-folding via tagnorm),
	// item3 -> kwB (via domains), item4 -> neither (total_scanned only).
	itemBase := nano % 1_000_000_000_000
	item1, item2, item3, item4 := itemBase+1, itemBase+2, itemBase+3, itemBase+4
	items := []struct {
		id       int64
		keywords string
		domains  string
	}{
		{item1, kwA + ",alignment", ""},
		{item2, "BEATKW A " + fmt.Sprintf("%d", nano), ""},
		{item3, "other-topic", kwB},
		{item4, "cooking", "food"},
	}
	for _, it := range items {
		if _, err := testutil.TestDB.Exec(
			`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES ($1, $2, 'beat coverage seed', $3)`,
			it.id, agentID, nowMs,
		); err != nil {
			t.Fatalf("failed to seed raw_items %d: %v", it.id, err)
		}
		if _, err := testutil.TestDB.Exec(
			`INSERT INTO processed_items (item_id, status, keywords, domains, updated_at) VALUES ($1, 3, $2, $3, $4)`,
			it.id, it.keywords, it.domains, nowMs,
		); err != nil {
			t.Fatalf("failed to seed processed_items %d: %v", it.id, err)
		}
	}

	// Delivery rows in replay_logs. Pushed must count only delivered=TRUE rows,
	// deduplicated by item_id:
	//   item1 TRUE twice (two impressions) -> counts once for kwA
	//   item1 NULL (pre-flag history)      -> ignored
	//   item2 FALSE (filtered, kwA item)   -> ignored, kwA pushed stays 1
	//   item3 TRUE                         -> counts once for kwB
	//   item4 TRUE                         -> no beat match
	deliveries := []struct {
		itemID    int64
		delivered interface{} // true / false / nil (NULL)
	}{
		{item1, true},
		{item1, true},
		{item1, nil},
		{item2, false},
		{item3, true},
		{item4, true},
	}
	for i, d := range deliveries {
		if _, err := testutil.TestDB.Exec(
			`INSERT INTO replay_logs (id, impression_id, agent_id, item_id, served_at, created_at, delivered)
			 VALUES ($1, $2, $3, $4, $5, $5, $6)`,
			itemBase*10+int64(i), fmt.Sprintf("imp-beat-%d-%d", nano, i), agentID, d.itemID, nowMs, d.delivered,
		); err != nil {
			t.Fatalf("failed to seed replay_logs %d: %v", d.itemID, err)
		}
	}

	// Kept (score>=1): item1 scored twice (1 then 2, kwA — must dedup to 1),
	// item3 score 2 (kwB); item4 score -1 (not kept).
	feedbacks := []struct {
		itemID int64
		score  int
	}{
		{item1, 1},
		{item1, 2},
		{item3, 2},
		{item4, -1},
	}
	for i, fb := range feedbacks {
		if _, err := testutil.TestDB.Exec(
			`INSERT INTO feedback_logs (stream_message_id, agent_id, item_id, score, feedback_at, created_at)
			 VALUES ($1, $2, $3, $4, $5, $5)`,
			fmt.Sprintf("beat-test-%d-%d", nano, i), agentID, fb.itemID, fb.score, nowMs,
		); err != nil {
			t.Fatalf("failed to seed feedback_logs %d: %v", fb.itemID, err)
		}
	}

	// replay_logs is not covered by testutil's cleanup; the rest is removed
	// too so reruns stay deterministic.
	defer func() {
		testutil.TestDB.Exec(`DELETE FROM replay_logs WHERE agent_id = $1`, agentID)
		testutil.TestDB.Exec(`DELETE FROM feedback_logs WHERE agent_id = $1`, agentID)
		testutil.TestDB.Exec(`DELETE FROM processed_items WHERE item_id IN ($1, $2, $3, $4)`, item1, item2, item3, item4)
		testutil.TestDB.Exec(`DELETE FROM raw_items WHERE item_id IN ($1, $2, $3, $4)`, item1, item2, item3, item4)
	}()

	// Drop the network signal cache so the seeded items are visible immediately.
	testutil.GetTestRedis().Del(context.Background(), "cache:beat_signals:7d")

	result := testutil.DoGet(t, "/api/v1/agents/me/beat_coverage?window=7d", token)
	assertCode(t, result, 0)

	data := result["data"].(map[string]interface{})
	if data["window"] != "7d" {
		t.Fatalf("expected window=7d, got %v", data["window"])
	}
	totalScanned := int64(data["total_scanned"].(float64))
	if totalScanned < 4 {
		t.Fatalf("expected total_scanned >= 4 (seeded items), got %d", totalScanned)
	}

	beats := data["beats"].([]interface{})
	if len(beats) != 2 {
		t.Fatalf("expected 2 beats, got %d: %v", len(beats), beats)
	}

	// Sorted by signals desc: kwA (2 signals) before kwB (1 signal).
	first := beats[0].(map[string]interface{})
	second := beats[1].(map[string]interface{})
	for _, key := range []string{"key", "name", "tier", "signals", "pushed", "kept"} {
		requireKey(t, first, key)
		requireKey(t, second, key)
	}

	if first["name"] != kwA {
		t.Fatalf("expected first beat %q (highest signals), got %v", kwA, first["name"])
	}
	if got := int64(first["signals"].(float64)); got != 2 {
		t.Fatalf("expected %s signals=2, got %d", kwA, got)
	}
	if got := int64(first["pushed"].(float64)); got != 1 {
		t.Fatalf("expected %s pushed=1, got %d", kwA, got)
	}
	if got := int64(first["kept"].(float64)); got != 1 {
		t.Fatalf("expected %s kept=1, got %d", kwA, got)
	}
	if first["tier"] != "hot" {
		t.Fatalf("expected %s tier=hot (ratio 1.0), got %v", kwA, first["tier"])
	}

	if second["name"] != kwB {
		t.Fatalf("expected second beat %q, got %v", kwB, second["name"])
	}
	if got := int64(second["signals"].(float64)); got != 1 {
		t.Fatalf("expected %s signals=1, got %d", kwB, got)
	}
	if got := int64(second["pushed"].(float64)); got != 1 {
		t.Fatalf("expected %s pushed=1, got %d", kwB, got)
	}
	if got := int64(second["kept"].(float64)); got != 1 {
		t.Fatalf("expected %s kept=1, got %d", kwB, got)
	}
	// ratio 1/2 = 0.5 -> active (>=0.45, <0.7)
	if second["tier"] != "active" {
		t.Fatalf("expected %s tier=active (ratio 0.5), got %v", kwB, second["tier"])
	}
}

// Two profile keywords that are separator variants of one another
// ("zbeat-<n>" and "zbeat <n>") must collapse into a SINGLE beat, keep a
// readable display name (not the separator-stripped norm), and count a
// spaced-form item exactly once — regression guard for the dedup-by-norm and
// display/norm split in GetBeatCoverage.
func TestBeatCoverageDedupsNormVariants(t *testing.T) {
	testutil.WaitForAPI(t)
	nano := time.Now().UnixNano()
	email := fmt.Sprintf("beat-dedup-%d@test.com", nano%1_000_000)
	token, agentID, _ := testutil.LoginAndGetToken(t, email)

	hyphen := fmt.Sprintf("zbeat-%d", nano) // -> zbeat<nano>
	spaced := fmt.Sprintf("zbeat %d", nano) // -> zbeat<nano> (same norm)
	nowMs := time.Now().UnixMilli()
	if _, err := testutil.TestDB.Exec(`
		INSERT INTO agent_profiles (agent_id, status, keywords, updated_at)
		VALUES ($1, 3, $2, $3)
		ON CONFLICT (agent_id) DO UPDATE SET keywords = $2, status = 3, updated_at = $3`,
		agentID, hyphen+","+spaced, nowMs,
	); err != nil {
		t.Fatalf("seed agent_profiles: %v", err)
	}

	itemID := (nano % 1_000_000_000_000) + 7
	if _, err := testutil.TestDB.Exec(
		`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES ($1, $2, 'dedup seed', $3)`,
		itemID, agentID, nowMs,
	); err != nil {
		t.Fatalf("seed raw_items: %v", err)
	}
	if _, err := testutil.TestDB.Exec(
		`INSERT INTO processed_items (item_id, status, keywords, domains, updated_at) VALUES ($1, 3, $2, '', $3)`,
		itemID, spaced, nowMs,
	); err != nil {
		t.Fatalf("seed processed_items: %v", err)
	}
	defer func() {
		testutil.TestDB.Exec(`DELETE FROM processed_items WHERE item_id = $1`, itemID)
		testutil.TestDB.Exec(`DELETE FROM raw_items WHERE item_id = $1`, itemID)
	}()

	testutil.GetTestRedis().Del(context.Background(), "cache:beat_signals:7d")

	result := testutil.DoGet(t, "/api/v1/agents/me/beat_coverage?window=7d", token)
	assertCode(t, result, 0)
	data := result["data"].(map[string]interface{})
	beats := data["beats"].([]interface{})
	if len(beats) != 1 {
		t.Fatalf("expected the 2 variant keywords to collapse into 1 beat, got %d: %v", len(beats), beats)
	}
	b := beats[0].(map[string]interface{})
	// Display name keeps a readable separator, not the stripped norm; key mirrors it.
	if b["name"] != hyphen {
		t.Fatalf("expected readable display name %q, got %v", hyphen, b["name"])
	}
	if b["key"] != b["name"] {
		t.Fatalf("expected key==name (both readable), got key=%v name=%v", b["key"], b["name"])
	}
	// The spaced-form item counted exactly once for the collapsed beat.
	if got := int64(b["signals"].(float64)); got != 1 {
		t.Fatalf("expected signals=1 (item counted once), got %d", got)
	}
}
