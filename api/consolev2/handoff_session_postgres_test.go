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

func TestHandoffSessionPostgres(t *testing.T) {
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
		`CREATE TEMP TABLE agents (agent_id BIGINT PRIMARY KEY, identity_state TEXT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_principals (principal_id BIGINT PRIMARY KEY, agent_id BIGINT, status TEXT, revoked_at BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE console_v2_sessions (session_id VARCHAR(128) PRIMARY KEY, agent_id BIGINT, principal_id BIGINT, session_secret_hash TEXT, csrf_secret_hash TEXT, status TEXT, scopes TEXT[], client_capabilities TEXT[], issued_at BIGINT, idle_expires_at BIGINT, absolute_expires_at BIGINT, last_seen_at BIGINT, auth_method TEXT, recent_auth_at BIGINT, revoked_at BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE console_v2_handoffs (ticket_hash TEXT PRIMARY KEY, agent_id BIGINT, principal_id BIGINT, console_scope TEXT[], client_capabilities TEXT[], browser_nonce_hash TEXT, expires_at BIGINT, consumed_at BIGINT, revoked_at BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_cli_account_switches (switch_id_hash TEXT, source_agent_id BIGINT, target_agent_id BIGINT, principal_id BIGINT, source_console_session_id TEXT, target_console_session_id TEXT, status TEXT, ownership_verified_at BIGINT, expires_at BIGINT, completed_at BIGINT, created_at BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_cli_account_switch_audit (switch_id_hash TEXT, source_agent_id BIGINT, target_agent_id BIGINT, principal_id BIGINT, result TEXT, occurred_at BIGINT) ON COMMIT DROP`,
	} {
		exec(query)
	}
	migration, err := os.ReadFile("../../migrations/000117_console_handoff_session.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	exec(up)
	exec(`INSERT INTO agents VALUES (17, 'active'), (42, 'active')`)
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
			firstCSRF := responseData(t, payload)["csrf_token"]
			sessionPair := cookiePair(cookies, consoleCookieName)
			csrfPair := cookiePair(cookies, csrfCookieName)
			switchPair := cookiePair(cookies, cliAccountSwitchCookieName)
			browser := sessionPair + "; " + csrfPair
			if switchPair != "" {
				browser += "; " + switchPair
			}
			var original struct {
				ConsumedAt        int64
				ConsumedSessionID string
			}
			if err := tx.Raw(`SELECT consumed_at,consumed_session_id FROM console_v2_handoffs WHERE ticket_hash=?`, hashString(ticket)).Scan(&original).Error; err != nil {
				t.Fatal(err)
			}
			if original.ConsumedAt == 0 || original.ConsumedSessionID == "" {
				t.Fatal("exchange did not bind its session")
			}
			for i := 0; i < 2; i++ {
				status, payload, replayCookies := request(ticket, nonce, browser)
				if status != 200 || responseData(t, payload)["csrf_token"] != firstCSRF || responseData(t, payload)["account_switch"] != switchFlow {
					t.Fatalf("resume: %d %#v", status, payload)
				}
				if cookiePair(replayCookies, consoleCookieName) != "" || cookiePair(replayCookies, csrfCookieName) != "" || cookiePair(replayCookies, cliAccountSwitchCookieName) != "" {
					t.Fatal("resume rotated credentials")
				}
			}
			// Find the original session even when another slot is currently selected.
			inactive := strings.Replace(sessionPair, consoleCookieName+"=", consoleSessionCookieName(2)+"=", 1) + "; " + strings.Replace(csrfPair, csrfCookieName+"=", consoleCSRFCookieName(2)+"=", 1) + "; " + activeConsoleSlotCookieName + "=0"
			if switchPair != "" {
				inactive += "; " + switchPair
			}
			status, _, activated := request(ticket, nonce, inactive)
			if status != 200 || cookiePair(activated, activeConsoleSlotCookieName) != activeConsoleSlotCookieName+"=2" {
				t.Fatalf("inactive slot resume: %d", status)
			}
			for _, tc := range []struct{ name, cookie, nonce string }{
				{"another browser", "", nonce}, {"wrong nonce", browser, strings.Repeat("x", 32)},
				{"forged session secret", strings.Replace(browser, sessionPair, consoleCookieName+"="+original.ConsumedSessionID+".wrong", 1), nonce},
				{"missing csrf", sessionPair, nonce},
			} {
				t.Run(tc.name, func(t *testing.T) {
					status, payload, _ := request(ticket, tc.nonce, tc.cookie)
					if status != 401 || responseErrorCode(t, payload) != "HANDOFF_INVALID" {
						t.Fatalf("%d %#v", status, payload)
					}
				})
			}
			// A different session for the same Agent cannot recover this ticket.
			exec(`INSERT INTO console_v2_sessions SELECT 'other',agent_id,principal_id,?,csrf_secret_hash,status,scopes,client_capabilities,issued_at,idle_expires_at,absolute_expires_at,last_seen_at,auth_method,recent_auth_at,revoked_at FROM console_v2_sessions WHERE session_id=?`, hashString("other-secret"), original.ConsumedSessionID)
			status, _, _ = request(ticket, nonce, strings.Replace(browser, sessionPair, consoleCookieName+"=other.other-secret", 1))
			if status != 401 {
				t.Fatal("accepted another session for same Agent")
			}
			exec(`DELETE FROM console_v2_sessions WHERE session_id='other'`)
			for _, tc := range []struct{ name, mutation string }{
				{"expired ticket", `UPDATE console_v2_handoffs SET expires_at=0`},
				{"revoked ticket", `UPDATE console_v2_handoffs SET revoked_at=1`},
				{"legacy unbound ticket", `UPDATE console_v2_handoffs SET consumed_session_id=NULL`},
				{"revoked session", `UPDATE console_v2_sessions SET status='revoked'`},
				{"expired session", `UPDATE console_v2_sessions SET idle_expires_at=0`},
				{"expired absolute session", `UPDATE console_v2_sessions SET absolute_expires_at=0`},
				{"revoked principal", `UPDATE agent_principals SET revoked_at=1 WHERE principal_id=1`},
				{"moved principal", `UPDATE agent_principals SET agent_id=42 WHERE principal_id=1`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					exec("SAVEPOINT invalid_replay")
					exec(tc.mutation)
					status, payload, _ := request(ticket, nonce, browser)
					if status != 401 || responseErrorCode(t, payload) != "HANDOFF_INVALID" {
						t.Fatalf("%d %#v", status, payload)
					}
					exec("ROLLBACK TO SAVEPOINT invalid_replay")
				})
			}
			if switchFlow {
				status, _, _ := request(ticket, nonce, sessionPair+"; "+csrfPair)
				if status != 401 {
					t.Fatal("resumed switch without its cookie")
				}
			}
			var count int64
			tx.Raw(`SELECT COUNT(*) FROM console_v2_sessions`).Scan(&count)
			if count != 1 {
				t.Fatalf("resume created sessions: %d", count)
			}
			var consumed int64
			tx.Raw(`SELECT consumed_at FROM console_v2_handoffs`).Scan(&consumed)
			if consumed != original.ConsumedAt {
				t.Fatal("resume changed consumption time")
			}
		})
	}
}
