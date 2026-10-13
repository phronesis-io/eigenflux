package consolev2

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/pkg/agentidentity"
)

func TestPostgresCommunicationSearchLiteralsCountsAndDeadline(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for message search PostgreSQL contracts")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	exec := func(sql string, args ...interface{}) {
		t.Helper()
		if err := tx.Exec(sql, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	base, now := time.Now().UnixNano(), time.Now().UnixMilli()
	for offset := int64(0); offset <= 7; offset++ {
		shortID, err := agentidentity.GenerateShortID()
		if err != nil {
			t.Fatal(err)
		}
		if offset == 1 {
			shortID = "AbCdE"
		}
		exec(`INSERT INTO agents (agent_id, short_id, email, agent_name, bio, created_at, updated_at)
			VALUES (?, ?, ?, 'Search Peer', '', ?, ?)`, base+offset, shortID,
			fmt.Sprintf("message-search-%d@example.test", base+offset), now, now)
		if offset == 0 {
			continue
		}
		exec(`INSERT INTO conversations (conv_id, participant_a, participant_b, initiator_id,
			last_sender_id, origin_type, origin_id, msg_count, status, updated_at, topic_status)
			VALUES (?, ?, ?, ?, ?, 'friend', 0, 1, 0, ?, 1)`,
			base+100+offset, base, base+offset, base, base+offset, now+offset)
		exec(`INSERT INTO private_messages (msg_id, conv_id, sender_id, receiver_id, content, is_read, created_at)
			VALUES (?, ?, ?, ?, 'unrelated message', false, ?)`,
			base+1000+offset, base+100+offset, base+offset, base, now+offset)
	}
	exec(`UPDATE agents SET agent_name='name_%' WHERE agent_id=?`, base+1)
	exec(`UPDATE agents SET agent_name_en='english!_' WHERE agent_id=?`, base+2)
	exec(`INSERT INTO user_relations (from_uid, to_uid, rel_type, remark, created_at)
		VALUES (?, ?, 1, 'remark%_', ?)`, base, base+3, now)
	exec(`UPDATE agents SET agent_name='nameXX', agent_name_en='english!' WHERE agent_id=?`, base+7)
	exec(`INSERT INTO user_relations (from_uid, to_uid, rel_type, remark, created_at)
		VALUES (?, ?, 1, 'remarkXX', ?)`, base, base+7, now)
	literalBody := `body!%_\`
	for offset := int64(1); offset <= 3; offset++ {
		exec(`INSERT INTO private_messages (msg_id, conv_id, sender_id, receiver_id, content, is_read, created_at)
			VALUES (?, ?, ?, ?, ?, true, ?)`, base+2000+offset, base+104, base+4, base,
			literalBody+" needle", now+100+offset)
	}
	exec(`UPDATE private_messages SET content='needle' WHERE conv_id=?`, base+105)
	exec(`UPDATE agents SET agent_name='needle' WHERE agent_id=?`, base+6)
	exec(`UPDATE private_messages SET content='body!XX' WHERE conv_id=?`, base+107)

	var readContexts []context.Context
	var slowRead bool
	var searchSQL string
	if err := db.Callback().Row().Before("gorm:row").Register("test:search_context", func(query *gorm.DB) {
		readContexts = append(readContexts, query.Statement.Context)
		if strings.Contains(query.Statement.SQL.String(), "WITH eligible AS") {
			searchSQL = query.Statement.SQL.String()
		}
		if slowRead {
			_, err := query.Statement.ConnPool.ExecContext(query.Statement.Context, "SELECT pg_sleep(1)")
			query.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{db: tx}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.GET("/search", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", base)
		if slowRead {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
			defer cancel()
		}
		svc.searchCommunicationMessages(ctx, c)
	})
	request := func(query url.Values) map[string]interface{} {
		t.Helper()
		status, payload, _ := performJSON(t, h, http.MethodGet, "/search?"+query.Encode(), nil)
		if status != http.StatusOK {
			t.Fatalf("status=%d payload=%#v", status, payload)
		}
		return responseData(t, payload)
	}
	results := func(data map[string]interface{}) []interface{} {
		t.Helper()
		rows, ok := data["results"].([]interface{})
		if !ok {
			t.Fatalf("search results must be a JSON array: %#v", data)
		}
		return rows
	}
	for _, test := range []struct {
		query, matchedBy string
		offset, count    int64
	}{
		{"AbCdE", "agent_name", 1, 0},
		{"  AbCdE  ", "agent_name", 1, 0},
		{"abcde", "", 0, 0},
		{"AbCd", "", 0, 0},
		{"AbCdEF", "", 0, 0},
		{"12345", "", 0, 0},
		{"AbCd1", "", 0, 0},
		{"AbC_E", "", 0, 0},
		{"AbC E", "", 0, 0},
		{"ÄbCdE", "", 0, 0},
		{"name_%", "agent_name", 1, 0},
		{"english!_", "agent_name", 2, 0},
		{"remark%_", "remark", 3, 0},
		{literalBody, "message", 4, 3},
		{"%%", "", 0, 0},
		{"__", "", 0, 0},
	} {
		t.Run(test.query, func(t *testing.T) {
			rows := results(request(url.Values{"q": {test.query}}))
			wantShortID := agentidentity.ValidShortID(strings.TrimSpace(test.query))
			if strings.Contains(searchSQL, "a.short_id") != wantShortID {
				t.Fatalf("short ID predicate must only run for five ASCII letters: query=%q SQL=%s", test.query, searchSQL)
			}
			if test.offset == 0 {
				if len(rows) != 0 {
					t.Fatalf("literal-only query matched unrelated messages: %#v", rows)
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("rows=%#v", rows)
			}
			row := rows[0].(map[string]interface{})
			if row["conv_id"] != strconv.FormatInt(base+100+test.offset, 10) ||
				row["peer_agent_id"] != strconv.FormatInt(base+test.offset, 10) ||
				row["matched_by"] != test.matchedBy || row["match_count"] != float64(test.count) {
				t.Fatalf("unexpected match: %#v", row)
			}
		})
	}
	exec(`UPDATE agents SET agent_name='needle collaborator' WHERE agent_id=?`, base+4)
	query := url.Values{"q": {"needle"}, "limit": {"1"}}
	for index, offset := range []int64{6, 4, 5} {
		page := request(query)
		rows := results(page)
		if len(rows) != 1 {
			t.Fatalf("page=%#v", page)
		}
		row := rows[0].(map[string]interface{})
		wantCount := []float64{0, 3, 1}[index]
		if row["conv_id"] != strconv.FormatInt(base+100+offset, 10) || row["match_count"] != wantCount || page["has_more"] != (index < 2) {
			t.Fatalf("page %d=%#v", index, page)
		}
		query.Set("cursor", page["next_cursor"].(string))
	}
	if len(readContexts) < 20 {
		t.Fatalf("expected main and enrichment database reads, observed %d", len(readContexts))
	}
	for _, ctx := range readContexts {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > communicationSearchTimeout || ctx.Err() == nil {
			t.Fatalf("search database read did not inherit its bounded request context: deadline=%v err=%v", deadline, ctx.Err())
		}
	}
	if tx.Statement.Context.Err() != nil {
		t.Fatal("search changed the shared database context")
	}

	// A shorter caller deadline must interrupt an in-flight PostgreSQL operation.
	slowRead = true
	started := time.Now()
	status, payload, _ := performJSON(t, h, http.MethodGet, "/search?q=needle", nil)
	if status != http.StatusGatewayTimeout || responseErrorCode(t, payload) != "MESSAGE_SEARCH_TIMEOUT" || time.Since(started) >= time.Second {
		t.Fatalf("database timeout was not propagated promptly: status=%d payload=%#v elapsed=%v", status, payload, time.Since(started))
	}
}

func TestMessageSearchMigrationPreservesExistingNameIndex(t *testing.T) {
	migration, err := os.ReadFile("../../migrations/000101_private_message_search.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, ok := strings.Cut(string(migration), "-- +goose Down")
	if !ok {
		t.Fatal("migration has no Down section")
	}
	if strings.Contains(down, "idx_agents_agent_name_trgm") {
		t.Fatal("search rollback must preserve the name index owned by migration 000001")
	}
}
