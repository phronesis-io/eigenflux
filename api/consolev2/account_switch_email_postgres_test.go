package consolev2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresCLIAccountSwitchEmail(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required")
	}
	if !strings.Contains(dsn, "127.0.0.1") && !strings.Contains(dsn, "localhost") {
		t.Fatal("loopback test database required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"new", "existing", "pending", "self", "wrong_otp", "expired", "wrong_browser", "other_active_account", "replayed", "http", "full_slots"} {
		t.Run(scenario, func(t *testing.T) {
			tx := db.Begin()
			defer tx.Rollback()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			s := &Service{db: tx, idgen: &fixedIDGenerator{id: time.Now().UnixNano()}, otpPepper: "switch-test", testOTP: "123456", testEmailPatterns: []string{"*@switch.test"}}
			now := time.Now().UnixMilli()
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			sourceEmail := fmt.Sprintf("source-%d@switch.test", time.Now().UnixNano())
			source, _, err := s.resolveSwitchEmail(tx, sourceEmail, now)
			check(err)
			var principal int64
			check(tx.Raw(`INSERT INTO agent_principals (agent_id,key_type,key_fingerprint,public_key,status,created_at,last_seen_at)
				VALUES (?, 'ed25519-v1', ?, decode(repeat('11',32),'hex'), 'active', ?, ?) RETURNING principal_id`, source, fmt.Sprint(source), now, now).Scan(&principal).Error)
			var peerPrincipal int64
			check(tx.Raw(`INSERT INTO agent_principals (agent_id,key_type,key_fingerprint,public_key,status,created_at,last_seen_at)
				VALUES (?, 'ed25519-v1', ?, decode(repeat('22',32),'hex'), 'active', ?, ?) RETURNING principal_id`, source, fmt.Sprint(source)+"peer", now, now).Scan(&peerPrincipal).Error)
			check(tx.Exec(`INSERT INTO agent_credential_sessions (principal_id,family_id,access_token_hash,refresh_token_hash,audience,scopes,issued_at,expires_at,absolute_expires_at,last_seen_at)
				VALUES (?, ?, ?, ?, 'agent-api', ARRAY['console:handoff:create'], ?, ?, ?, ?)`, principal, fmt.Sprint(source), fmt.Sprint(source), fmt.Sprint(source), now, now+3600000, now+7200000, now).Error)
			session := fmt.Sprintf("efcs_%d", source)
			check(tx.Exec(`INSERT INTO console_v2_sessions (session_id,session_secret_hash,agent_id,principal_id,csrf_secret_hash,status,scopes,issued_at,idle_expires_at,absolute_expires_at,last_seen_at,auth_method)
				VALUES (?, ?, ?, ?, ?, 'active', ARRAY['console:onboarding','console:read','console:write'], ?, ?, ?, ?, 'handoff')`, session, hashString("secret"), source, principal, hashString("csrf"), now, now+3600000, now+7200000, now).Error)
			token := fmt.Sprintf("efas_%d", source)
			check(tx.Exec(`INSERT INTO agent_cli_account_switches (switch_id_hash,source_agent_id,principal_id,source_console_session_id,status,expires_at,created_at)
				VALUES (?, ?, ?, ?, 'pending_target', ?, ?)`, hashString(token), source, principal, session, now+3600000, now).Error)
			email := fmt.Sprintf("target-%d@switch.test", source)
			var target int64
			if scenario == "existing" || scenario == "pending" {
				target, _, err = s.resolveSwitchEmail(tx, email, now)
				check(err)
				if scenario == "existing" {
					check(tx.Exec(`INSERT INTO agent_context_revisions (agent_id,revision,compiled_context,schema_version,generated_at) VALUES (?,1,'{}',1,?)`, target, now).Error)
					check(tx.Exec(`UPDATE agent_onboarding_v2 SET state='completed', current_step=5,active_context_revision=1,completed_at=? WHERE agent_id=?`, now, target).Error)
				}
			}
			if scenario == "self" {
				email, target = sourceEmail, source
			}
			extraCookies := map[string]string{}
			if scenario == "full_slots" {
				for i := 1; i < 5; i++ {
					id, _, err := s.resolveSwitchEmail(tx, fmt.Sprintf("slot-%d-%d@switch.test", source, i), now)
					check(err)
					var pid int64
					check(tx.Raw(`INSERT INTO agent_principals (agent_id,key_type,key_fingerprint,public_key,status,created_at,last_seen_at) VALUES (?, 'email-recovery-v1', ?, decode(repeat('33',32),'hex'), 'limited', ?, ?) RETURNING principal_id`, id, fmt.Sprint(id), now, now).Scan(&pid).Error)
					sid := fmt.Sprintf("efcs_slot_%d", id)
					check(tx.Exec(`INSERT INTO console_v2_sessions (session_id,session_secret_hash,agent_id,principal_id,csrf_secret_hash,status,scopes,issued_at,idle_expires_at,absolute_expires_at,last_seen_at,auth_method) VALUES (?, ?, ?, ?, ?, 'active', '{}', ?, ?, ?, ?, 'email_otp')`, sid, hashString(sid), id, pid, hashString("csrf"), now, now+3600000, now+7200000, now).Error)
					extraCookies[fmt.Sprintf("%s_%d", consoleCookieName, i)] = sid + "." + sid
				}
			}
			request := func(body interface{}) *app.RequestContext {
				c := app.NewContext(0)
				c.Request.Header.SetCookie(cliAccountSwitchCookieName, token)
				c.Request.Header.SetCookie(consoleCookieName, session+".secret")
				for name, value := range extraCookies {
					c.Request.Header.SetCookie(name, value)
				}
				c.Set("agent_id", source)
				c.Set("console_session_id", session)
				encoded, _ := json.Marshal(body)
				c.Request.SetBody(encoded)
				return c
			}
			challenge := request(map[string]string{"email": email})
			s.createCLIAccountSwitchChallenge(context.Background(), challenge)
			if challenge.Response.StatusCode() != 202 {
				t.Fatalf("challenge: %s", challenge.Response.Body())
			}
			var envelope struct {
				Data struct {
					ChallengeID string `json:"challenge_id"`
				} `json:"data"`
			}
			check(json.Unmarshal(challenge.Response.Body(), &envelope))
			code := "123456"
			if scenario == "wrong_otp" {
				code = "000000"
			}
			if scenario == "expired" {
				check(tx.Exec(`UPDATE agent_cli_account_switches SET expires_at=? WHERE switch_id_hash=?`, now-1, hashString(token)).Error)
			}
			verify := request(map[string]string{"email": email, "challenge_id": envelope.Data.ChallengeID, "otp": code})
			if scenario == "wrong_browser" {
				verify.Request.Header.SetCookie(consoleCookieName, session+".wrong")
			}
			if scenario == "other_active_account" {
				verify.Set("agent_id", int64(42))
				verify.Set("console_session_id", "another-browser-account")
			}
			if scenario == "http" {
				s.publicURL = "https://console.example.test"
				h := server.Default()
				h.POST("/api/v2/console/account-switch/verify", s.consoleAuth(true), s.verifyCLIAccountSwitchEmail)
				headers := []ut.Header{{Key: "Cookie", Value: consoleCookieName + "=" + session + ".secret; " + cliAccountSwitchCookieName + "=" + token}}
				body := map[string]string{"email": email, "challenge_id": envelope.Data.ChallengeID, "otp": code}
				code, _, _ := performJSON(t, h, "POST", "/api/v2/console/account-switch/verify", body, headers...)
				if code != 403 {
					t.Fatal("missing CSRF accepted")
				}
				headers = append(headers, ut.Header{Key: "X-CSRF-Token", Value: "csrf"})
				code, payload, _ := performJSON(t, h, "POST", "/api/v2/console/account-switch/verify", body, headers...)
				verify.Response.SetStatusCode(code)
				encoded, _ := json.Marshal(payload)
				verify.Response.SetBody(encoded)
			} else {
				s.verifyCLIAccountSwitchEmail(context.Background(), verify)
			}
			var actual int64
			check(tx.Raw(`SELECT agent_id FROM agent_principals WHERE principal_id=?`, principal).Scan(&actual).Error)
			if scenario == "wrong_otp" || scenario == "expired" || scenario == "wrong_browser" {
				if verify.Response.StatusCode() != 401 || actual != source {
					t.Fatalf("failure changed source or unexpected response: %s", verify.Response.Body())
				}
				if scenario == "wrong_otp" {
					var attempts int
					check(tx.Raw(`SELECT attempt_count FROM v2_email_challenges WHERE challenge_id=?`, envelope.Data.ChallengeID).Scan(&attempts).Error)
					if attempts != 1 {
						t.Fatal("OTP attempt not committed")
					}
				}
				return
			}
			if verify.Response.StatusCode() != 200 {
				t.Fatalf("verify: %s", verify.Response.Body())
			}
			var status string
			check(tx.Raw(`SELECT status FROM agent_cli_account_switches WHERE switch_id_hash=?`, hashString(token)).Scan(&status).Error)
			var peerAccount int64
			check(tx.Raw(`SELECT agent_id FROM agent_principals WHERE principal_id=?`, peerPrincipal).Scan(&peerAccount).Error)
			if peerAccount != source {
				t.Fatal("another Agent's login moved")
			}
			if scenario != "self" {
				record, err := loadCLIAccountSwitch(tx, token, false)
				check(err)
				get := request(nil)
				get.Set("agent_id", *record.TargetAgentID)
				get.Set("console_session_id", record.TargetConsoleSession)
				s.getCLIAccountSwitch(context.Background(), get)
				var result map[string]interface{}
				check(json.Unmarshal(get.Response.Body(), &result))
				if responseData(t, result)["can_continue_onboarding"] != true {
					t.Fatalf("cannot resume target: %s", get.Response.Body())
				}
				get.Set("console_session_id", "unrelated-session")
				get.Response.Reset()
				s.getCLIAccountSwitch(context.Background(), get)
				check(json.Unmarshal(get.Response.Body(), &result))
				if responseData(t, result)["can_continue_onboarding"] != false {
					t.Fatal("unrelated session can resume")
				}
			}
			if scenario == "pending" {
				if actual != source || status != "pending_onboarding" {
					t.Fatal("existing incomplete target switched early")
				}
				return
			}
			if scenario == "self" {
				if actual != source || status != "completed_noop" {
					t.Fatal("self switch not a no-op")
				}
				return
			}
			if status != "completed" || actual == source || (target != 0 && actual != target) {
				t.Fatal("CLI principal did not move to target")
			}
			var originalEmail string
			check(tx.Raw(`SELECT email FROM agents WHERE agent_id=?`, source).Scan(&originalEmail).Error)
			if originalEmail != sourceEmail {
				t.Fatal("source account email changed")
			}
			var refresh bool
			check(tx.Raw(`SELECT access_refresh_required FROM agent_credential_sessions WHERE principal_id=?`, principal).Scan(&refresh).Error)
			if !refresh {
				t.Fatal("CLI credential refresh not requested")
			}
			if scenario != "existing" {
				var principalStatus string
				check(tx.Raw(`SELECT status FROM agent_principals WHERE principal_id=?`, principal).Scan(&principalStatus).Error)
				if principalStatus != "limited" {
					t.Fatal("new account inherited source privileges")
				}
			}
			if scenario == "replayed" {
				again := request(map[string]string{"email": email, "challenge_id": envelope.Data.ChallengeID, "otp": code})
				s.verifyCLIAccountSwitchEmail(context.Background(), again)
				if again.Response.StatusCode() == 200 {
					t.Fatal("replayed proof accepted")
				}
			}
		})
	}
}
