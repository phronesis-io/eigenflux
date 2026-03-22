package testutil

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"

	"eigenflux_server/pkg/config"
)

var TestDB *sql.DB

func InitDB() {
	cfg := config.Load()
	var err error
	TestDB, err = sql.Open("pgx", cfg.PgDSN)
	if err != nil {
		panic("failed to connect to database: " + err.Error())
	}
}

// CleanTestData removes all test data from DB, Redis and ES.
func CleanTestData(t *testing.T, emails ...string) {
	t.Helper()
	ctx := context.Background()
	rdb := GetTestRedis()

	if len(emails) == 0 {
		rows, err := TestDB.Query(`
			SELECT DISTINCT email FROM (
				SELECT email FROM agents WHERE email LIKE '%@test.com'
				UNION
				SELECT email FROM auth_email_challenges WHERE email LIKE '%@test.com'
			) e
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var email string
				if scanErr := rows.Scan(&email); scanErr == nil && email != "" {
					emails = append(emails, email)
				}
			}
		}
		if len(emails) == 0 {
			emails = []string{"author@test.com", "user@test.com"}
		}
	}

	// --- ES ---
	cleanESAllItems(t)

	// --- DB ---
	// Collect agent_ids before deleting
	var agentIDs []int64
	for _, email := range emails {
		var agentID int64
		err := TestDB.QueryRow("SELECT agent_id FROM agents WHERE email = $1", email).Scan(&agentID)
		if err != nil {
			continue
		}
		agentIDs = append(agentIDs, agentID)
		TestDB.Exec("DELETE FROM item_stats WHERE item_id IN (SELECT item_id FROM raw_items WHERE author_agent_id = $1)", agentID)
		TestDB.Exec("DELETE FROM agent_sessions WHERE agent_id = $1", agentID)
		TestDB.Exec("DELETE FROM processed_items WHERE item_id IN (SELECT item_id FROM raw_items WHERE author_agent_id = $1)", agentID)
		TestDB.Exec("DELETE FROM raw_items WHERE author_agent_id = $1", agentID)
		TestDB.Exec("DELETE FROM agent_profiles WHERE agent_id = $1", agentID)
		TestDB.Exec("DELETE FROM notification_deliveries WHERE agent_id = $1", agentID)
		TestDB.Exec("DELETE FROM milestone_events WHERE author_agent_id = $1", agentID)
		TestDB.Exec("DELETE FROM agents WHERE agent_id = $1", agentID)
	}
	for _, email := range emails {
		TestDB.Exec("DELETE FROM auth_email_challenges WHERE email = $1", email)
	}
	resetMilestoneRules()
	TestDB.Exec("DELETE FROM system_notifications")

	// --- Redis ---
	// Auth keys
	for _, email := range emails {
		h := sha256.Sum256([]byte(email))
		emailHash := hex.EncodeToString(h[:])
		rdb.Del(ctx, "auth:login:email:cooldown:"+emailHash)
	}
	for _, ip := range []string{"127.0.0.1", "::1", "[::1]"} {
		rdb.Del(ctx, "auth:login:start:email:ip:"+ip)
		rdb.Del(ctx, "auth:login:verify:email:ip:"+ip)
	}

	// Per-agent keys: impr, feed cache
	for _, agentID := range agentIDs {
		rdb.Del(ctx, fmt.Sprintf("impr:agent:%d:items", agentID))
		rdb.Del(ctx, fmt.Sprintf("impr:agent:%d:groups", agentID))
		rdb.Del(ctx, fmt.Sprintf("impr:agent:%d:urls", agentID))
		rdb.Del(ctx, fmt.Sprintf("feed:cache:%d", agentID))
		rdb.Del(ctx, fmt.Sprintf("milestone:notify:%d", agentID))
	}

	// Global keys by pattern: bloom filter, search cache, profile cache
	cleanRedisKeysByPattern(ctx, rdb, "bf:global:*")
	cleanRedisKeysByPattern(ctx, rdb, "cache:search:*")
	cleanRedisKeysByPattern(ctx, rdb, "cache:profile:*")
	cleanRedisKeysByPattern(ctx, rdb, "milestone:notify:*")
	cleanRedisKeysByPattern(ctx, rdb, "notify:system:*")
	cleanRedisKeysByPattern(ctx, rdb, "notify:pending:*")

	t.Log("Test data cleaned (DB + ES + Redis)")
}

func resetMilestoneRules() {
	nowExpr := "EXTRACT(EPOCH FROM NOW())::BIGINT * 1000"
	TestDB.Exec("DELETE FROM milestone_events")
	TestDB.Exec("DELETE FROM milestone_rules")
	TestDB.Exec(`
		INSERT INTO milestone_rules (metric_key, threshold, rule_enabled, content_template, created_at, updated_at)
		VALUES
			('consumed', 50, TRUE, 'Your Content "{{.ItemSummary}}" reached {{.CounterValue}} consumptions. Item Id {{.ItemID}}', ` + nowExpr + `, ` + nowExpr + `),
			('consumed', 500, TRUE, 'Your Content "{{.ItemSummary}}" reached {{.CounterValue}} consumptions. Item Id {{.ItemID}}', ` + nowExpr + `, ` + nowExpr + `),
			('score_1', 50, TRUE, 'Your Content "{{.ItemSummary}}" reached {{.CounterValue}} score_1 ratings. Item Id {{.ItemID}}', ` + nowExpr + `, ` + nowExpr + `),
			('score_1', 500, TRUE, 'Your Content "{{.ItemSummary}}" reached {{.CounterValue}} score_1 ratings. Item Id {{.ItemID}}', ` + nowExpr + `, ` + nowExpr + `),
			('score_2', 50, TRUE, 'Your Content "{{.ItemSummary}}" reached {{.CounterValue}} score_2 ratings. Item Id {{.ItemID}}', ` + nowExpr + `, ` + nowExpr + `),
			('score_2', 500, TRUE, 'Your Content "{{.ItemSummary}}" reached {{.CounterValue}} score_2 ratings. Item Id {{.ItemID}}', ` + nowExpr + `, ` + nowExpr + `)
	`)
}

// cleanRedisKeysByPattern deletes all Redis keys matching the given pattern.
func cleanRedisKeysByPattern(ctx context.Context, rdb *redis.Client, pattern string) {
	keys, err := rdb.Keys(ctx, pattern).Result()
	if err != nil || len(keys) == 0 {
		return
	}
	rdb.Del(ctx, keys...)
}

// RefreshES forces an Elasticsearch refresh so recently indexed documents become searchable.
// ES default refresh_interval is 30s; tests need immediate visibility.
func RefreshES(t *testing.T) {
	t.Helper()
	esPort := os.Getenv("ELASTICSEARCH_HTTP_PORT")
	if esPort == "" {
		esPort = "9200"
	}
	esURL := fmt.Sprintf("http://localhost:%s", esPort)

	req, err := http.NewRequest("POST", esURL+"/items-*/_refresh", nil)
	if err != nil {
		t.Logf("Failed to create ES refresh request: %v", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("Failed to refresh ES: %v", err)
		return
	}
	resp.Body.Close()
	t.Log("ES index refreshed")
}

// cleanESAllItems deletes all documents from the items ES index.
func cleanESAllItems(t *testing.T) {
	t.Helper()
	esPort := os.Getenv("ELASTICSEARCH_HTTP_PORT")
	if esPort == "" {
		esPort = "9200"
	}
	esURL := fmt.Sprintf("http://localhost:%s", esPort)

	body := `{"query":{"match_all":{}}}`
	req, err := http.NewRequest("POST", esURL+"/items-*/_delete_by_query?refresh=true", strings.NewReader(body))
	if err != nil {
		t.Logf("Failed to create ES delete_by_query request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("Failed to clean ES items: %v", err)
		return
	}
	resp.Body.Close()
	t.Log("Cleaned all items from ES")
}
