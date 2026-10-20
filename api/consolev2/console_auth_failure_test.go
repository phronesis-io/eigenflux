package consolev2

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestConsoleAuthDatabaseFailuresRemainUnavailable(t *testing.T) {
	for _, tc := range []struct{ name, target, reason string }{
		{"lookup", "SELECT s.session_id", "session_lookup_failed"},
		{"renewal", "UPDATE console_v2_sessions SET last_seen_at", "session_renewal_failed"},
		{"revocation", "UPDATE console_v2_sessions SET status", "session_revocation_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBrowserSessionFixture(t)
			sessionID := f.add(t, 0, 10, false)
			f.add(t, 1, 20, false)
			if tc.name == "revocation" {
				if err := f.service.db.Exec(`UPDATE agent_principals SET revoked_at=1 WHERE agent_id=10`).Error; err != nil {
					t.Fatal(err)
				}
			} else if tc.name == "renewal" {
				if err := f.service.db.Exec(`UPDATE console_v2_sessions SET last_seen_at=? WHERE session_id=?`, f.now-int64(6*time.Minute/time.Millisecond), sessionID).Error; err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			f.service.db = f.service.db.Session(&gorm.Session{Logger: gormlogger.New(log.New(&logs, "", 0), gormlogger.Config{LogLevel: gormlogger.Info})})
			ctx := context.WithValue(context.Background(), refreshContextKey{}, "present")
			inject := func(query *gorm.DB) {
				if query.Statement.Context.Value(refreshContextKey{}) != "present" {
					t.Error("Console authentication lost request context")
				}
				if strings.Contains(query.Statement.SQL.String(), tc.target) {
					query.AddError(&pgconn.PgError{Code: "08006", Message: "password=console-secret SELECT session-secret csrf-secret"})
				}
			}
			if err := f.service.db.Callback().Row().Before("gorm:row").Register("test:console_database_failure", inject); err != nil {
				t.Fatal(err)
			}
			if err := f.service.db.Callback().Raw().Before("gorm:raw").Register("test:console_database_failure", inject); err != nil {
				t.Fatal(err)
			}
			h := server.New()
			called := false
			h.GET("/read", func(_ context.Context, c *app.RequestContext) { f.service.consoleAuth(false)(ctx, c) }, func(_ context.Context, c *app.RequestContext) {
				called = true
				reply(c, 200, map[string]bool{"allowed": true})
			})
			status, payload, _ := performJSON(t, h, http.MethodGet, "/read", nil, ut.Header{Key: "Cookie", Value: f.cookieHeader(0)})
			if status != 503 || responseErrorCode(t, payload) != "CONSOLE_AUTH_UNAVAILABLE" || called {
				t.Fatalf("Console database failure=%d %#v called=%v", status, payload, called)
			}
			var record map[string]interface{}
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
				t.Fatalf("expected one sanitized JSON failure: %v %s", err, logs.String())
			}
			if record["reason"] != tc.reason || record["stage"] != "console" || record["sql_state"] != "08006" {
				t.Fatalf("missing Console failure cause: %#v", record)
			}
			for _, secret := range []string{"password=", "console-secret", "session-secret", "csrf-secret", "SELECT", hashString("secret"), hashString("csrf")} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("Console auth leaked %s", secret)
				}
			}
		})
	}
}

func TestConsoleAuthInvalidSessionsRemainUnauthorized(t *testing.T) {
	for _, tc := range []struct{ name, cookie, mutation, code string }{
		{"missing", "", "", "CONSOLE_SESSION_REQUIRED"},
		{"wrong secret", consoleCookieName + "=session-10-0.wrong", "", "CONSOLE_SESSION_INVALID"},
		{"expired", consoleCookieName + "=session-10-0.secret", "UPDATE console_v2_sessions SET idle_expires_at=0", "CONSOLE_SESSION_INVALID"},
		{"revoked", consoleCookieName + "=session-10-0.secret", "UPDATE console_v2_sessions SET status='revoked'", "CONSOLE_SESSION_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBrowserSessionFixture(t)
			f.add(t, 0, 10, false)
			if tc.mutation != "" {
				if err := f.service.db.Exec(tc.mutation).Error; err != nil {
					t.Fatal(err)
				}
			}
			h := server.New()
			h.GET("/read", f.service.consoleAuth(false), func(_ context.Context, c *app.RequestContext) { t.Error("invalid session reached handler") })
			status, payload, _ := performJSON(t, h, http.MethodGet, "/read", nil, ut.Header{Key: "Cookie", Value: tc.cookie})
			if status != 401 || responseErrorCode(t, payload) != tc.code {
				t.Fatalf("invalid session=%d %#v", status, payload)
			}
		})
	}
}

func TestConsoleAuthSlidingExpiryAndConcurrentRenewal(t *testing.T) {
	for _, competingRenewal := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "concurrent"}[competingRenewal], func(t *testing.T) {
			f := newBrowserSessionFixture(t)
			sessionID := f.add(t, 0, 10, false)
			lastSeen := f.now - int64(6*time.Minute/time.Millisecond)
			absolute := f.now + int64(60*24*time.Hour/time.Millisecond)
			if err := f.service.db.Exec(`UPDATE console_v2_sessions SET last_seen_at=?,absolute_expires_at=? WHERE session_id=?`, lastSeen, absolute, sessionID).Error; err != nil {
				t.Fatal(err)
			}
			if competingRenewal {
				if err := f.service.db.Callback().Raw().Before("gorm:raw").Register("test:concurrent_console_renewal", func(query *gorm.DB) {
					if strings.Contains(query.Statement.SQL.String(), "SET last_seen_at") {
						// Simulate a concurrent request winning the last_seen_at CAS.
						query.Statement.SQL.Reset()
						query.Statement.SQL.WriteString("UPDATE console_v2_sessions SET last_seen_at=last_seen_at WHERE 1=0")
						query.Statement.Vars = nil
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			h := server.New()
			h.GET("/read", f.service.consoleAuth(false), func(_ context.Context, c *app.RequestContext) { reply(c, 200, map[string]bool{"allowed": true}) })
			status, payload, _ := performJSON(t, h, http.MethodGet, "/read", nil, ut.Header{Key: "Cookie", Value: f.cookieHeader(0)})
			if status != 200 {
				t.Fatalf("renewal=%d %#v", status, payload)
			}
			if !competingRenewal {
				var stored struct{ LastSeenAt, IdleExpiresAt, AbsoluteExpiresAt int64 }
				if err := f.service.db.Raw(`SELECT last_seen_at,idle_expires_at,absolute_expires_at FROM console_v2_sessions WHERE session_id=?`, sessionID).Scan(&stored).Error; err != nil {
					t.Fatal(err)
				}
				if stored.IdleExpiresAt-stored.LastSeenAt != int64(consoleIdleTTL/time.Millisecond) || stored.AbsoluteExpiresAt != absolute {
					t.Fatalf("Console renewal changed its lifetime contract: %+v", stored)
				}
			}
		})
	}
}
