package consolev2

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestHandoffReusablePostgres(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("loopback PG_DSN required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("loopback PostgreSQL required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	exec := func(query string, args ...interface{}) {
		t.Helper()
		if err := tx.Exec(query, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`CREATE TEMP TABLE agents (agent_id BIGINT PRIMARY KEY, identity_state TEXT, agent_name TEXT, agent_name_en TEXT, short_id TEXT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_email_bindings (agent_id BIGINT, normalized_email TEXT, status TEXT, verification_state TEXT, updated_at BIGINT, binding_id BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_principals (principal_id BIGINT PRIMARY KEY, agent_id BIGINT, status TEXT, revoked_at BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE console_v2_sessions (session_id VARCHAR(128) PRIMARY KEY, agent_id BIGINT, principal_id BIGINT, session_secret_hash TEXT, csrf_secret_hash TEXT, status TEXT, scopes TEXT[], client_capabilities TEXT[], issued_at BIGINT, idle_expires_at BIGINT, absolute_expires_at BIGINT, last_seen_at BIGINT, auth_method TEXT, recent_auth_at BIGINT, revoked_at BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE console_v2_handoffs (ticket_hash TEXT PRIMARY KEY, agent_id BIGINT, principal_id BIGINT, console_scope TEXT[], client_capabilities TEXT[], browser_nonce_hash TEXT, expires_at BIGINT, consumed_at BIGINT, revoked_at BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_cli_account_switches (switch_id_hash TEXT, source_agent_id BIGINT, target_agent_id BIGINT, principal_id BIGINT, source_console_session_id TEXT, target_console_session_id TEXT, status TEXT, ownership_verified_at BIGINT, expires_at BIGINT, completed_at BIGINT, created_at BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_cli_account_switch_audit (switch_id_hash TEXT, source_agent_id BIGINT, target_agent_id BIGINT, principal_id BIGINT, result TEXT, occurred_at BIGINT) ON COMMIT DROP`,
	} {
		exec(query)
	}
	exec(`INSERT INTO agents (agent_id,identity_state) VALUES (17, 'active'), (42, 'active')`)
	exec(`INSERT INTO agent_principals VALUES (1,17,'active',NULL),(2,42,'active',NULL)`)
	svc := &Service{db: tx, publicURL: "https://console.example.test"}
	h := server.New()
	h.POST("/api/v2/console/handoffs/exchange", svc.requireSameOrigin(), svc.exchangeHandoff)
	nonce := strings.Repeat("n", 32)
	request := func(ticket, requestNonce, cookies string) (int, map[string]interface{}, [][]byte) {
		return performJSON(t, h, http.MethodPost, "/api/v2/console/handoffs/exchange", map[string]interface{}{"ticket": ticket, "browser_nonce": requestNonce},
			ut.Header{Key: "Origin", Value: "https://console.example.test"}, ut.Header{Key: "Cookie", Value: cookies})
	}
	for _, switchFlow := range []bool{false, true} {
		t.Run(fmt.Sprintf("switch=%v", switchFlow), func(t *testing.T) {
			exec(`DELETE FROM console_v2_handoffs`)
			exec(`DELETE FROM console_v2_sessions`)
			exec(`DELETE FROM agent_cli_account_switches`)
			now := time.Now().UnixMilli()
			ticket := fmt.Sprintf("efht_test_%v", switchFlow)
			capabilities := "{}"
			if switchFlow {
				capabilities = "{account_switch_v1}"
			}
			exec(`INSERT INTO console_v2_handoffs (ticket_hash,agent_id,principal_id,console_scope,client_capabilities,browser_nonce_hash,expires_at) VALUES (?,17,1,ARRAY['console:read'],?::text[],?,?)`, hashString(ticket), capabilities, hashString(nonce), now+int64(handoffTTL/time.Millisecond))
			status, payload, cookies := request(ticket, nonce, "")
			if status != 200 {
				t.Fatalf("first exchange: %d %#v", status, payload)
			}
			firstSession := cookiePair(cookies, consoleCookieName)
			browser := firstSession + "; " + cookiePair(cookies, csrfCookieName)
			var firstConsumed int64
			tx.Raw(`SELECT consumed_at FROM console_v2_handoffs WHERE ticket_hash=?`, hashString(ticket)).Scan(&firstConsumed)
			if firstConsumed == 0 {
				t.Fatal("first exchange not recorded")
			}
			// A fresh browser can use an already exchanged link, without any cookies.
			status, payload, secondCookies := request(ticket, nonce, "")
			if status != 200 || responseData(t, payload)["account_switch"] != switchFlow {
				t.Fatalf("another browser: %d %#v", status, payload)
			}
			if cookiePair(secondCookies, consoleCookieName) == firstSession {
				t.Fatal("browsers shared credentials")
			}
			// A refresh/retry from the first browser can also exchange again.
			status, payload, _ = request(ticket, nonce, browser)
			if status != 200 {
				t.Fatalf("same browser retry: %d %#v", status, payload)
			}
			var consumed int64
			tx.Raw(`SELECT consumed_at FROM console_v2_handoffs WHERE ticket_hash=?`, hashString(ticket)).Scan(&consumed)
			if consumed != firstConsumed {
				t.Fatal("first exchange timestamp changed")
			}
			for _, tc := range []struct{ name, ticket, nonce, mutation, code string }{
				{"missing ticket", "efht_missing", nonce, "", "HANDOFF_NOT_FOUND"},
				{"wrong nonce", ticket, strings.Repeat("x", 32), "", "HANDOFF_NONCE_INVALID"},
				{"expired", ticket, nonce, `UPDATE console_v2_handoffs SET expires_at=0`, "HANDOFF_EXPIRED"},
				{"revoked", ticket, nonce, `UPDATE console_v2_handoffs SET revoked_at=1`, "HANDOFF_REVOKED"},
				{"revoked principal", ticket, nonce, `UPDATE agent_principals SET revoked_at=1 WHERE principal_id=1`, "HANDOFF_IDENTITY_INVALID"},
				{"moved principal", ticket, nonce, `UPDATE agent_principals SET agent_id=42 WHERE principal_id=1`, "HANDOFF_IDENTITY_INVALID"},
				{"inactive identity", ticket, nonce, `UPDATE agents SET identity_state='recovered_temporary' WHERE agent_id=17`, "HANDOFF_IDENTITY_INVALID"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					exec("SAVEPOINT invalid_exchange")
					if tc.mutation != "" {
						exec(tc.mutation)
					}
					var before, after int64
					tx.Raw(`SELECT COUNT(*) FROM console_v2_sessions`).Scan(&before)
					status, payload, _ := request(tc.ticket, tc.nonce, "")
					if status != 401 || responseErrorCode(t, payload) != tc.code {
						t.Fatalf("%d %#v", status, payload)
					}
					tx.Raw(`SELECT COUNT(*) FROM console_v2_sessions`).Scan(&after)
					if before != after {
						t.Fatal("failed exchange created a session")
					}
					exec("ROLLBACK TO SAVEPOINT invalid_exchange")
				})
			}
		})
	}
}
