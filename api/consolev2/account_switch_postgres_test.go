package consolev2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/pkg/agentidentity"
)

// This regression exercises PostgreSQL's non-deferrable switch-state CHECK
// constraints. SQLite/unit tests cannot detect the production failure where a
// pending_target row was given a target before it became completed.
func TestPostgresCLIAccountSwitchCompletesAtomically(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}

	type fixture struct {
		record      cliAccountSwitchRecord
		sourceID    int64
		targetID    int64
		principalID int64
	}
	seed := func(label string, validTargetSession, storedTarget bool) fixture {
		t.Helper()
		now := time.Now().UnixMilli()
		base := time.Now().UnixNano()
		sourceID, targetID := base, base+1
		sourceShortID, shortErr := agentidentity.GenerateShortID()
		if shortErr != nil {
			t.Fatal(shortErr)
		}
		targetShortID, shortErr := agentidentity.GenerateShortID()
		if shortErr != nil {
			t.Fatal(shortErr)
		}
		if err := db.Exec(`INSERT INTO agents
			(agent_id, short_id, email, email_kind, agent_name, bio, created_at, updated_at)
			VALUES (?, ?, ?, 'internal_alias', 'Switch Source', '', ?, ?),
			       (?, ?, ?, 'v2_bound', 'Switch Target', '', ?, ?)`,
			sourceID, sourceShortID, fmt.Sprintf("switch-source-%d@agent.eigenflux.internal", sourceID), now, now,
			targetID, targetShortID, fmt.Sprintf("switch-target-%d@example.test", targetID), now, now).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			db.Exec(`DELETE FROM agent_cli_account_switch_audit WHERE source_agent_id = ?`, sourceID)
			db.Exec(`DELETE FROM agent_cli_account_switches WHERE source_agent_id = ?`, sourceID)
			db.Exec(`DELETE FROM console_v2_sessions WHERE agent_id IN (?, ?)`, sourceID, targetID)
			db.Exec(`DELETE FROM agents WHERE agent_id IN (?, ?)`, sourceID, targetID)
		})

		var sourcePrincipalID, targetPrincipalID int64
		if err := db.Raw(`INSERT INTO agent_principals
			(agent_id, key_type, key_fingerprint, public_key, key_version, status, created_at, last_seen_at)
			VALUES (?, 'ed25519-v1', ?, decode(repeat('11', 32), 'hex'), 1, 'active', ?, ?)
			RETURNING principal_id`, sourceID, fmt.Sprintf("switch-source-%d", sourceID), now, now).Scan(&sourcePrincipalID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Raw(`INSERT INTO agent_principals
			(agent_id, key_type, key_fingerprint, public_key, key_version, status, created_at, last_seen_at)
			VALUES (?, 'ed25519-v1', ?, decode(repeat('22', 32), 'hex'), 1, 'active', ?, ?)
			RETURNING principal_id`, targetID, fmt.Sprintf("switch-target-%d", targetID), now, now).Scan(&targetPrincipalID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO agent_credential_sessions
			(principal_id, family_id, access_token_hash, refresh_token_hash, audience, scopes,
			 issued_at, expires_at, absolute_expires_at, last_seen_at)
			VALUES (?, ?, ?, ?, 'agent-api', ARRAY['console:handoff:create'], ?, ?, ?, ?)`,
			sourcePrincipalID, fmt.Sprintf("switch-family-%d", sourceID), fmt.Sprintf("switch-access-%d", sourceID),
			fmt.Sprintf("switch-refresh-%d", sourceID), now, now+3600000, now+7200000, now).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO agent_context_revisions
			(agent_id, revision, compiled_context, schema_version, generated_at)
			VALUES (?, 1, '{}'::jsonb, 1, ?)`, targetID, now).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO agent_onboarding_v2
			(agent_id, state, current_step, revision, active_context_revision, completed_at, created_at, updated_at)
			VALUES (?, 'completed', 5, 1, 1, ?, ?, ?)`, targetID, now, now, now).Error; err != nil {
			t.Fatal(err)
		}

		sourceSession := fmt.Sprintf("switch-source-session-%d", sourceID)
		targetSession := fmt.Sprintf("switch-target-session-%d", targetID)
		if err := db.Exec(`INSERT INTO console_v2_sessions
			(session_id, session_secret_hash, agent_id, principal_id, csrf_secret_hash, status, scopes,
			 issued_at, idle_expires_at, absolute_expires_at, last_seen_at, auth_method, recent_auth_at)
			VALUES (?, ?, ?, ?, ?, 'active', '{}'::text[], ?, ?, ?, ?, 'handoff', NULL)`,
			sourceSession, fmt.Sprintf("switch-source-secret-%d", sourceID), sourceID, sourcePrincipalID,
			fmt.Sprintf("switch-source-csrf-%d", sourceID), now, now+3600000, now+7200000, now).Error; err != nil {
			t.Fatal(err)
		}
		if validTargetSession {
			if err := db.Exec(`INSERT INTO console_v2_sessions
				(session_id, session_secret_hash, agent_id, principal_id, csrf_secret_hash, status, scopes,
				 issued_at, idle_expires_at, absolute_expires_at, last_seen_at, auth_method, recent_auth_at)
				VALUES (?, ?, ?, ?, ?, 'active', '{}'::text[], ?, ?, ?, ?, 'email_otp', ?)`,
				targetSession, fmt.Sprintf("switch-target-secret-%d", targetID), targetID, targetPrincipalID,
				fmt.Sprintf("switch-target-csrf-%d", targetID), now, now+3600000, now+7200000, now, now).Error; err != nil {
				t.Fatal(err)
			}
		}

		switchHash := fmt.Sprintf("switch-hash-%s-%d", label, sourceID)
		status := "pending_target"
		if storedTarget {
			status = "pending_onboarding"
			if err := db.Exec(`INSERT INTO agent_cli_account_switches
				(switch_id_hash, source_agent_id, target_agent_id, principal_id, source_console_session_id,
				 target_console_session_id, status, ownership_verified_at, expires_at, created_at)
				VALUES (?, ?, ?, ?, ?, ?, 'pending_onboarding', ?, ?, ?)`, switchHash, sourceID, targetID,
				sourcePrincipalID, sourceSession, targetSession, now, now+3600000, now).Error; err != nil {
				t.Fatal(err)
			}
		} else if err := db.Exec(`INSERT INTO agent_cli_account_switches
			(switch_id_hash, source_agent_id, principal_id, source_console_session_id,
			 status, expires_at, created_at)
			VALUES (?, ?, ?, ?, 'pending_target', ?, ?)`, switchHash, sourceID, sourcePrincipalID,
			sourceSession, now+3600000, now).Error; err != nil {
			t.Fatal(err)
		}
		verifiedAt := now
		return fixture{
			record: cliAccountSwitchRecord{
				SwitchIDHash: switchHash, SourceAgentID: sourceID, TargetAgentID: &targetID,
				PrincipalID: sourcePrincipalID, SourceConsoleSession: sourceSession,
				TargetConsoleSession: targetSession, Status: status,
				OwnershipVerifiedAt: &verifiedAt, ExpiresAt: now + 3600000,
			},
			sourceID: sourceID, targetID: targetID, principalID: sourcePrincipalID,
		}
	}

	t.Run("completed target moves directly from pending_target to completed", func(t *testing.T) {
		fx := seed("success", true, false)
		now := time.Now().UnixMilli()
		if err := db.Transaction(func(tx *gorm.DB) error {
			return finalizeCLIAccountSwitch(tx, fx.record, now)
		}); err != nil {
			t.Fatalf("atomic completion failed: %v", err)
		}
		var result struct {
			Status              string `gorm:"column:status"`
			TargetAgentID       *int64 `gorm:"column:target_agent_id"`
			OwnershipVerifiedAt *int64 `gorm:"column:ownership_verified_at"`
			CompletedAt         *int64 `gorm:"column:completed_at"`
		}
		if err := db.Raw(`SELECT status, target_agent_id, ownership_verified_at, completed_at
			FROM agent_cli_account_switches WHERE switch_id_hash = ?`, fx.record.SwitchIDHash).Scan(&result).Error; err != nil {
			t.Fatal(err)
		}
		if result.Status != "completed" || result.TargetAgentID == nil || *result.TargetAgentID != fx.targetID ||
			result.OwnershipVerifiedAt == nil || result.CompletedAt == nil {
			t.Fatalf("switch did not reach a complete terminal state: %#v", result)
		}
		var principalAgentID int64
		if err := db.Raw(`SELECT agent_id FROM agent_principals WHERE principal_id = ?`, fx.principalID).Scan(&principalAgentID).Error; err != nil {
			t.Fatal(err)
		}
		if principalAgentID != fx.targetID {
			t.Fatalf("principal agent_id=%d, want %d", principalAgentID, fx.targetID)
		}
		var refreshed bool
		if err := db.Raw(`SELECT access_refresh_required FROM agent_credential_sessions
			WHERE principal_id = ?`, fx.principalID).Scan(&refreshed).Error; err != nil || !refreshed {
			t.Fatalf("credential refresh flag=%v err=%v", refreshed, err)
		}
		var audits int64
		if err := db.Raw(`SELECT COUNT(*) FROM agent_cli_account_switch_audit
			WHERE switch_id_hash = ? AND result = 'completed'`, fx.record.SwitchIDHash).Scan(&audits).Error; err != nil || audits != 1 {
			t.Fatalf("completed audits=%d err=%v", audits, err)
		}
	})

	t.Run("current account completes without moving credentials", func(t *testing.T) {
		fx := seed("noop", false, false)
		fx.record.TargetAgentID = nil
		fx.record.TargetConsoleSession = ""
		fx.record.OwnershipVerifiedAt = nil
		now := time.Now().UnixMilli()
		if err := db.Transaction(func(tx *gorm.DB) error {
			return completeNoopCLIAccountSwitch(tx, fx.record, now)
		}); err != nil {
			t.Fatalf("no-op completion failed: %v", err)
		}
		var result struct {
			Status        string `gorm:"column:status"`
			TargetAgentID *int64 `gorm:"column:target_agent_id"`
			CompletedAt   *int64 `gorm:"column:completed_at"`
		}
		if err := db.Raw(`SELECT status, target_agent_id, completed_at
			FROM agent_cli_account_switches WHERE switch_id_hash = ?`, fx.record.SwitchIDHash).Scan(&result).Error; err != nil {
			t.Fatal(err)
		}
		if result.Status != "completed_noop" || result.TargetAgentID != nil || result.CompletedAt == nil {
			t.Fatalf("same-account switch did not reach no-op terminal state: %#v", result)
		}
		var principalAgentID int64
		if err := db.Raw(`SELECT agent_id FROM agent_principals WHERE principal_id = ?`, fx.principalID).Scan(&principalAgentID).Error; err != nil {
			t.Fatal(err)
		}
		if principalAgentID != fx.sourceID {
			t.Fatalf("no-op switch moved principal to %d", principalAgentID)
		}
		var refreshed bool
		if err := db.Raw(`SELECT access_refresh_required FROM agent_credential_sessions
			WHERE principal_id = ?`, fx.principalID).Scan(&refreshed).Error; err != nil || refreshed {
			t.Fatalf("no-op switch refresh flag=%v err=%v", refreshed, err)
		}
		var audits int64
		if err := db.Raw(`SELECT COUNT(*) FROM agent_cli_account_switch_audit
			WHERE switch_id_hash = ? AND result = 'completed_noop'`, fx.record.SwitchIDHash).Scan(&audits).Error; err != nil || audits != 1 {
			t.Fatalf("completed_noop audits=%d err=%v", audits, err)
		}
	})

	t.Run("current account handler needs no recent email auth and is idempotent", func(t *testing.T) {
		fx := seed("handler-noop", false, false)
		token := fmt.Sprintf("efas_handler-noop-%d", fx.sourceID)
		if err := db.Exec(`UPDATE agent_cli_account_switches SET switch_id_hash = ?
			WHERE switch_id_hash = ?`, hashString(token), fx.record.SwitchIDHash).Error; err != nil {
			t.Fatal(err)
		}
		fx.record.SwitchIDHash = hashString(token)
		service := &Service{db: db}

		confirm := func() map[string]interface{} {
			t.Helper()
			request := app.NewContext(0)
			request.Request.Header.Set("Cookie", cliAccountSwitchCookieName+"="+token)
			request.Set("agent_id", fx.sourceID)
			request.Set("console_session_id", fx.record.SourceConsoleSession)
			service.confirmCLIAccountSwitch(context.Background(), request)
			if request.Response.StatusCode() != http.StatusOK {
				t.Fatalf("status = %d, body = %s", request.Response.StatusCode(), request.Response.Body())
			}
			var envelope struct {
				Data map[string]interface{} `json:"data"`
			}
			if err := json.Unmarshal(request.Response.Body(), &envelope); err != nil {
				t.Fatal(err)
			}
			setCookies := request.Response.Header.PeekAll("Set-Cookie")
			parsedHeaders := http.Header{}
			for _, cookie := range setCookies {
				parsedHeaders.Add("Set-Cookie", string(cookie))
			}
			cleared := false
			for _, cookie := range (&http.Response{Header: parsedHeaders}).Cookies() {
				if cookie.Name == cliAccountSwitchCookieName && cookie.Value == "" &&
					(cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && cookie.Expires.Before(time.Now()))) {
					cleared = true
				}
			}
			if !cleared {
				t.Fatalf("completed response did not clear switch cookie: %q", setCookies)
			}
			return envelope.Data
		}

		for attempt := 1; attempt <= 2; attempt++ {
			data := confirm()
			if data["status"] != "completed" || data["agent_id"] != fmt.Sprintf("%d", fx.sourceID) ||
				data["already_current"] != true || data["refresh_required"] != false {
				t.Fatalf("attempt %d returned unexpected no-op response: %#v", attempt, data)
			}
			if attempt == 1 {
				if err := db.Exec(`UPDATE agent_cli_account_switches SET expires_at = ?
					WHERE switch_id_hash = ?`, time.Now().Add(-time.Minute).UnixMilli(), fx.record.SwitchIDHash).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
		var audits int64
		if err := db.Raw(`SELECT COUNT(*) FROM agent_cli_account_switch_audit
			WHERE switch_id_hash = ? AND result = 'completed_noop'`, fx.record.SwitchIDHash).Scan(&audits).Error; err != nil || audits != 1 {
			t.Fatalf("idempotent completed_noop audits=%d err=%v", audits, err)
		}
	})

	t.Run("current account completion rejects a principal already moved elsewhere", func(t *testing.T) {
		fx := seed("noop-moved", false, false)
		if err := db.Exec(`UPDATE agent_principals SET agent_id = ? WHERE principal_id = ?`, fx.targetID, fx.principalID).Error; err != nil {
			t.Fatal(err)
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			return completeNoopCLIAccountSwitch(tx, fx.record, time.Now().UnixMilli())
		})
		if err == nil {
			t.Fatal("no-op completed after the principal moved to another Agent")
		}
		var status string
		if err := db.Raw(`SELECT status FROM agent_cli_account_switches
			WHERE switch_id_hash = ?`, fx.record.SwitchIDHash).Scan(&status).Error; err != nil {
			t.Fatal(err)
		}
		if status != "pending_target" {
			t.Fatalf("rejected no-op left status=%q", status)
		}
	})

	t.Run("onboarding completion retains the verified pending target", func(t *testing.T) {
		fx := seed("onboarding", true, true)
		if err := db.Transaction(func(tx *gorm.DB) error {
			return finalizeCLIAccountSwitch(tx, fx.record, time.Now().UnixMilli())
		}); err != nil {
			t.Fatalf("pending onboarding completion failed: %v", err)
		}
		var status string
		var targetID int64
		if err := db.Raw(`SELECT status, target_agent_id FROM agent_cli_account_switches
			WHERE switch_id_hash = ?`, fx.record.SwitchIDHash).Row().Scan(&status, &targetID); err != nil {
			t.Fatal(err)
		}
		if status != "completed" || targetID != fx.targetID {
			t.Fatalf("pending onboarding completed as status=%q target=%d", status, targetID)
		}
	})

	t.Run("terminal write failure rolls back principal and credentials", func(t *testing.T) {
		fx := seed("rollback", false, false)
		err := db.Transaction(func(tx *gorm.DB) error {
			return finalizeCLIAccountSwitch(tx, fx.record, time.Now().UnixMilli())
		})
		if err == nil {
			t.Fatal("missing target Console session unexpectedly completed")
		}
		var principalAgentID int64
		if err := db.Raw(`SELECT agent_id FROM agent_principals WHERE principal_id = ?`, fx.principalID).Scan(&principalAgentID).Error; err != nil {
			t.Fatal(err)
		}
		if principalAgentID != fx.sourceID {
			t.Fatalf("failed transaction moved principal to %d", principalAgentID)
		}
		var refreshed bool
		if err := db.Raw(`SELECT access_refresh_required FROM agent_credential_sessions
			WHERE principal_id = ?`, fx.principalID).Scan(&refreshed).Error; err != nil || refreshed {
			t.Fatalf("failed transaction refresh flag=%v err=%v", refreshed, err)
		}
		var status string
		var targetID *int64
		if err := db.Raw(`SELECT status, target_agent_id FROM agent_cli_account_switches
			WHERE switch_id_hash = ?`, fx.record.SwitchIDHash).Row().Scan(&status, &targetID); err != nil {
			t.Fatal(err)
		}
		if status != "pending_target" || targetID != nil {
			t.Fatalf("failed transaction left status=%q target=%v", status, targetID)
		}
	})
}
