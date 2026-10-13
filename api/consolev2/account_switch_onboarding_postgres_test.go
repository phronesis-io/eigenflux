package consolev2

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

// Continue the real OTP switch fixture through onboarding, including the
// completed switch produced when the target email creates a new account.
func testSwitchOnboardingContinuation(t *testing.T, s *Service, token string, principal int64) {
	t.Helper()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	record, err := loadCLIAccountSwitch(s.db, token, false)
	check(err)
	id := *record.TargetAgentID
	draft := `{"identity_card":{"agent_name":"Switch continuation","bio":"Onboarding regression"},"security_boundary":{"recurring_publish":false,"auto_reply_pm":false,"auto_comment":false,"show_add_friend":true},"network_goal":"Find infrastructure signals","intent_actions":[{"watch_for":"infrastructure updates","trigger_when":"source is relevant","action_instruction":"analyze and report","action_policy":"analyze_only","priority":10}]}`
	check(s.db.Exec(`UPDATE agent_onboarding_drafts SET draft_data = ?::jsonb WHERE agent_id = ? AND revision = 1`, draft, id).Error)
	confirm := func(agent int64, session string, step int16, revision int64, key string) *app.RequestContext {
		c := app.NewContext(0)
		c.Set("agent_id", agent)
		c.Set("console_session_id", session)
		c.Request.Header.SetCookie(cliAccountSwitchCookieName, token)
		body, err := json.Marshal(confirmStepRequest{Step: step, ExpectedOnboardingRevision: revision, IdempotencyKey: key})
		check(err)
		c.Request.SetBody(body)
		s.confirmOnboardingStep(context.Background(), c)
		return c
	}
	for _, scenario := range []string{"wrong_session", "wrong_agent", "unverified", "expired_time", "revoked", "expired_status"} {
		t.Run("onboarding_rejects_"+scenario, func(t *testing.T) {
			check(s.db.SavePoint("onboarding_rejection").Error)
			defer func() { check(s.db.RollbackTo("onboarding_rejection").Error) }()
			agent, session := id, record.TargetConsoleSession
			switch scenario {
			case "wrong_session":
				session = record.SourceConsoleSession
			case "wrong_agent":
				agent = record.SourceAgentID
			case "unverified":
				check(s.db.Exec(`UPDATE agent_cli_account_switches SET ownership_verified_at = NULL WHERE switch_id_hash = ?`, record.SwitchIDHash).Error)
			case "expired_time":
				check(s.db.Exec(`UPDATE agent_cli_account_switches SET expires_at = ? WHERE switch_id_hash = ?`, time.Now().UnixMilli()-1, record.SwitchIDHash).Error)
			case "revoked", "expired_status":
				status := "revoked"
				if scenario == "expired_status" {
					status = "expired"
				}
				check(s.db.Exec(`UPDATE agent_cli_account_switches SET status = ?, completed_at = NULL WHERE switch_id_hash = ?`, status, record.SwitchIDHash).Error)
			}
			c := confirm(agent, session, 2, 1, "rejected-"+scenario)
			var payload map[string]interface{}
			check(json.Unmarshal(c.Response.Body(), &payload))
			if c.Response.StatusCode() != 409 || responseErrorCode(t, payload) != "EMAIL_BINDING_REQUIRED" {
				t.Fatalf("unexpected rejection: %s", c.Response.Body())
			}
		})
	}
	for step := int16(2); step <= 5; step++ {
		revision := int64(step - 1)
		key := fmt.Sprintf("switch-onboarding-%d", step)
		c := confirm(id, record.TargetConsoleSession, step, revision, key)
		if c.Response.StatusCode() != 200 {
			t.Fatalf("confirm step %d: %s", step, c.Response.Body())
		}
		replayed := confirm(id, record.TargetConsoleSession, step, revision, key)
		if replayed.Response.StatusCode() != 200 || string(replayed.Response.Body()) != string(c.Response.Body()) {
			t.Fatalf("step %d replay changed result: %s", step, replayed.Response.Body())
		}
	}
	var state struct {
		State                 string
		Revision              int64
		ActiveContextRevision *int64
	}
	check(s.db.Raw(`SELECT state, revision, active_context_revision FROM agent_onboarding_v2 WHERE agent_id = ?`, id).Scan(&state).Error)
	if state.State != "completed" || state.Revision != 5 || state.ActiveContextRevision == nil {
		t.Fatalf("onboarding not completed exactly once: %+v", state)
	}
	var identity struct {
		AgentID int64
		Status  string
	}
	check(s.db.Raw(`SELECT agent_id, status FROM agent_principals WHERE principal_id = ?`, principal).Scan(&identity).Error)
	if identity.AgentID != id || identity.Status != "active" {
		t.Fatalf("CLI principal not activated on target: %+v", identity)
	}
	var completedAudits int64
	check(s.db.Raw(`SELECT COUNT(*) FROM agent_cli_account_switch_audit WHERE switch_id_hash = ? AND result = 'completed'`, record.SwitchIDHash).Scan(&completedAudits).Error)
	if completedAudits != 1 {
		t.Fatalf("switch completed %d times", completedAudits)
	}
}
