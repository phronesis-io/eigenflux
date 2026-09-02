package consolev2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const (
	cliAccountSwitchCookieName = "ef_cli_account_switch"
	cliAccountSwitchTTL        = 24 * time.Hour
)

type cliAccountSwitchRecord struct {
	SwitchIDHash         string `gorm:"column:switch_id_hash"`
	SourceAgentID        int64  `gorm:"column:source_agent_id"`
	TargetAgentID        *int64 `gorm:"column:target_agent_id"`
	PrincipalID          int64  `gorm:"column:principal_id"`
	SourceConsoleSession string `gorm:"column:source_console_session_id"`
	TargetConsoleSession string `gorm:"column:target_console_session_id"`
	Status               string `gorm:"column:status"`
	OwnershipVerifiedAt  *int64 `gorm:"column:ownership_verified_at"`
	ExpiresAt            int64  `gorm:"column:expires_at"`
	CompletedAt          *int64 `gorm:"column:completed_at"`
}

func (s *Service) setCLIAccountSwitchCookie(c *app.RequestContext, value string, maxAge int) {
	c.SetCookie(cliAccountSwitchCookieName, value, maxAge, "/", "", protocol.CookieSameSiteStrictMode, s.secureCookie, true)
}

func cliAccountSwitchToken(c *app.RequestContext) string {
	return strings.TrimSpace(string(c.Cookie(cliAccountSwitchCookieName)))
}

func loadCLIAccountSwitch(tx *gorm.DB, token string, forUpdate bool) (cliAccountSwitchRecord, error) {
	var record cliAccountSwitchRecord
	query := `SELECT switch_id_hash, source_agent_id, target_agent_id, principal_id,
		source_console_session_id, COALESCE(target_console_session_id, '') AS target_console_session_id,
		status, ownership_verified_at, expires_at, completed_at
		FROM agent_cli_account_switches WHERE switch_id_hash = ?`
	if forUpdate {
		query += " FOR UPDATE"
	}
	err := tx.Raw(query, hashString(token)).Scan(&record).Error
	if err != nil || record.SwitchIDHash == "" {
		return cliAccountSwitchRecord{}, errUnauthorized
	}
	return record, nil
}

func auditCLIAccountSwitch(tx *gorm.DB, record cliAccountSwitchRecord, result string, now int64) error {
	return tx.Exec(`INSERT INTO agent_cli_account_switch_audit
		(switch_id_hash, source_agent_id, target_agent_id, principal_id, result, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?)`, record.SwitchIDHash, record.SourceAgentID,
		record.TargetAgentID, record.PrincipalID, result, now).Error
}

func expireCLIAccountSwitch(tx *gorm.DB, record cliAccountSwitchRecord, now int64) error {
	if record.Status != "pending_target" && record.Status != "pending_onboarding" {
		return nil
	}
	res := tx.Exec(`UPDATE agent_cli_account_switches SET status = 'expired'
		WHERE switch_id_hash = ? AND status IN ('pending_target', 'pending_onboarding')`, record.SwitchIDHash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errConflict
	}
	return auditCLIAccountSwitch(tx, record, "expired", now)
}

func finalizeCLIAccountSwitch(tx *gorm.DB, record cliAccountSwitchRecord, now int64) error {
	if record.SwitchIDHash == "" || record.SourceAgentID <= 0 || record.PrincipalID <= 0 ||
		record.TargetAgentID == nil || *record.TargetAgentID <= 0 || *record.TargetAgentID == record.SourceAgentID ||
		record.TargetConsoleSession == "" ||
		record.OwnershipVerifiedAt == nil || *record.OwnershipVerifiedAt <= 0 || *record.OwnershipVerifiedAt > now {
		return errConflict
	}
	var principal struct {
		AgentID int64  `gorm:"column:agent_id"`
		KeyType string `gorm:"column:key_type"`
		Status  string `gorm:"column:status"`
	}
	if err := tx.Raw(`SELECT agent_id, key_type, status FROM agent_principals
		WHERE principal_id = ? FOR UPDATE`, record.PrincipalID).Scan(&principal).Error; err != nil {
		return err
	}
	if principal.AgentID != record.SourceAgentID || principal.KeyType != "ed25519-v1" ||
		(principal.Status != "limited" && principal.Status != "active") {
		return errConflict
	}
	var target struct {
		IdentityState   string `gorm:"column:identity_state"`
		OnboardingState string `gorm:"column:onboarding_state"`
	}
	if err := tx.Raw(`SELECT agents.identity_state, onboarding.state AS onboarding_state
		FROM agents JOIN agent_onboarding_v2 onboarding ON onboarding.agent_id = agents.agent_id
		WHERE agents.agent_id = ? FOR UPDATE OF agents, onboarding`, *record.TargetAgentID).Scan(&target).Error; err != nil {
		return err
	}
	if target.IdentityState != "active" || target.OnboardingState != "completed" {
		return errOnboardingRequired
	}
	var activeCredentials int64
	if err := tx.Raw(`SELECT COUNT(*) FROM agent_credential_sessions
		WHERE principal_id = ? AND revoked_at IS NULL AND expires_at > ?`, record.PrincipalID, now).Scan(&activeCredentials).Error; err != nil {
		return err
	}
	if activeCredentials == 0 {
		return errConflict
	}
	res := tx.Exec(`UPDATE agent_principals SET agent_id = ?, status = 'active', last_seen_at = ?
		WHERE principal_id = ? AND agent_id = ? AND revoked_at IS NULL`, *record.TargetAgentID, now,
		record.PrincipalID, record.SourceAgentID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errConflict
	}
	if err := tx.Exec(`UPDATE agent_credential_sessions SET scopes = ?, access_refresh_required = TRUE
		WHERE principal_id = ? AND revoked_at IS NULL`, pq.Array(principalScopesForOnboarding("completed")), record.PrincipalID).Error; err != nil {
		return err
	}
	// Persist the verified target binding and the terminal state in one
	// statement. A pending_target row deliberately has no target, and the
	// database CHECK constraint must never observe a fabricated intermediate
	// pending_onboarding state for an already-completed target account.
	res = tx.Exec(`UPDATE agent_cli_account_switches
		SET target_agent_id = ?, target_console_session_id = ?, ownership_verified_at = ?,
			status = 'completed', completed_at = ?
		WHERE switch_id_hash = ? AND source_agent_id = ? AND principal_id = ?
		  AND status IN ('pending_target', 'pending_onboarding')`, *record.TargetAgentID,
		record.TargetConsoleSession, *record.OwnershipVerifiedAt, now, record.SwitchIDHash,
		record.SourceAgentID, record.PrincipalID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errConflict
	}
	record.Status = "completed"
	completedAt := now
	record.CompletedAt = &completedAt
	return auditCLIAccountSwitch(tx, record, "completed", now)
}

func (s *Service) getCLIAccountSwitch(_ context.Context, c *app.RequestContext) {
	token := cliAccountSwitchToken(c)
	if !strings.HasPrefix(token, "efas_") {
		fail(c, http.StatusNotFound, "ACCOUNT_SWITCH_NOT_FOUND", "CLI account switch is not available", nil)
		return
	}
	record, err := loadCLIAccountSwitch(s.db, token, false)
	if err != nil {
		fail(c, http.StatusNotFound, "ACCOUNT_SWITCH_NOT_FOUND", "CLI account switch is not available", nil)
		return
	}
	now := time.Now().UnixMilli()
	if (record.Status == "pending_target" || record.Status == "pending_onboarding") && record.ExpiresAt < now {
		_ = s.db.Transaction(func(tx *gorm.DB) error { return expireCLIAccountSwitch(tx, record, now) })
		record.Status = "expired"
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"status": record.Status, "source_agent_id": fmt.Sprintf("%d", record.SourceAgentID),
		"target_agent_id": func() string {
			if record.TargetAgentID == nil {
				return ""
			}
			return fmt.Sprintf("%d", *record.TargetAgentID)
		}(), "expires_at": record.ExpiresAt,
	})
}

func (s *Service) confirmCLIAccountSwitch(_ context.Context, c *app.RequestContext) {
	token := cliAccountSwitchToken(c)
	targetAgentID, targetOK := agentID(c)
	targetSessionValue, sessionOK := c.Get("console_session_id")
	targetSessionID, sessionTypeOK := targetSessionValue.(string)
	now := time.Now().UnixMilli()
	if !strings.HasPrefix(token, "efas_") || !targetOK || !sessionOK || !sessionTypeOK || targetSessionID == "" {
		fail(c, http.StatusBadRequest, "ACCOUNT_SWITCH_INVALID", "CLI account switch is invalid or expired", nil)
		return
	}
	if !requireRecentEmailAuth(c, now) {
		fail(c, http.StatusForbidden, "RECENT_EMAIL_AUTH_REQUIRED", "verify the target account email again before switching the CLI account", map[string]interface{}{"window_seconds": int(recentEmailAuthWindow / time.Second)})
		return
	}
	status := ""
	resultAgentID := targetAgentID
	expired := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		record, err := loadCLIAccountSwitch(tx, token, true)
		if err != nil {
			return err
		}
		if record.ExpiresAt < now {
			if err := expireCLIAccountSwitch(tx, record, now); err != nil {
				return err
			}
			expired = true
			return nil
		}
		if record.Status == "completed" {
			if record.TargetAgentID == nil || *record.TargetAgentID != targetAgentID {
				return errConflict
			}
			resultAgentID = *record.TargetAgentID
			status = "completed"
			return nil
		}
		if record.Status != "pending_target" && record.Status != "pending_onboarding" {
			return errUnauthorized
		}
		if targetAgentID == record.SourceAgentID {
			return errConflict
		}
		if record.Status == "pending_onboarding" && (record.TargetAgentID == nil || *record.TargetAgentID != targetAgentID || record.TargetConsoleSession != targetSessionID) {
			return errConflict
		}
		var onboardingState string
		if err := tx.Raw(`SELECT state FROM agent_onboarding_v2 WHERE agent_id = ? FOR UPDATE`, targetAgentID).Scan(&onboardingState).Error; err != nil {
			return err
		}
		if onboardingState == "completed" {
			target := targetAgentID
			verifiedAt := now
			record.TargetAgentID = &target
			record.TargetConsoleSession = targetSessionID
			record.OwnershipVerifiedAt = &verifiedAt
			if err := finalizeCLIAccountSwitch(tx, record, now); err != nil {
				return err
			}
			status = "completed"
			return nil
		}
		if record.Status == "pending_target" {
			res := tx.Exec(`UPDATE agent_cli_account_switches SET target_agent_id = ?,
				target_console_session_id = ?, ownership_verified_at = ?, status = 'pending_onboarding'
				WHERE switch_id_hash = ? AND status = 'pending_target'`, targetAgentID, targetSessionID, now, record.SwitchIDHash)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return errConflict
			}
			target := targetAgentID
			record.TargetAgentID = &target
			record.TargetConsoleSession = targetSessionID
			record.Status = "pending_onboarding"
			if err := auditCLIAccountSwitch(tx, record, "pending_onboarding", now); err != nil {
				return err
			}
		}
		status = "pending_onboarding"
		return nil
	})
	if errors.Is(err, errUnauthorized) {
		fail(c, http.StatusUnauthorized, "ACCOUNT_SWITCH_INVALID", "CLI account switch is invalid or expired", nil)
		return
	}
	if errors.Is(err, errConflict) {
		fail(c, http.StatusConflict, "ACCOUNT_SWITCH_CONFLICT", "the selected account cannot be used for this CLI switch", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "ACCOUNT_SWITCH_FAILED", "could not switch the CLI account", nil)
		return
	}
	if expired {
		fail(c, http.StatusUnauthorized, "ACCOUNT_SWITCH_INVALID", "CLI account switch is invalid or expired", nil)
		return
	}
	if status == "completed" {
		s.setCLIAccountSwitchCookie(c, "", -1)
		reply(c, http.StatusOK, map[string]interface{}{"status": status, "agent_id": fmt.Sprintf("%d", resultAgentID), "refresh_required": true})
		return
	}
	reply(c, http.StatusAccepted, map[string]interface{}{
		"status": "pending_onboarding", "agent_id": fmt.Sprintf("%d", targetAgentID),
		"requires_onboarding": true, "current_cli_account_unchanged": true,
	})
}

func (s *Service) cancelCLIAccountSwitch(_ context.Context, c *app.RequestContext) {
	token := cliAccountSwitchToken(c)
	if !strings.HasPrefix(token, "efas_") {
		fail(c, http.StatusNotFound, "ACCOUNT_SWITCH_NOT_FOUND", "CLI account switch is not available", nil)
		return
	}
	now := time.Now().UnixMilli()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		record, err := loadCLIAccountSwitch(tx, token, true)
		if err != nil {
			return err
		}
		if record.Status != "pending_target" && record.Status != "pending_onboarding" {
			return errConflict
		}
		if err := tx.Exec(`UPDATE agent_cli_account_switches SET status = 'revoked'
			WHERE switch_id_hash = ?`, record.SwitchIDHash).Error; err != nil {
			return err
		}
		return auditCLIAccountSwitch(tx, record, "revoked", now)
	})
	if err != nil {
		fail(c, http.StatusConflict, "ACCOUNT_SWITCH_NOT_PENDING", "CLI account switch is not pending", nil)
		return
	}
	s.setCLIAccountSwitchCookie(c, "", -1)
	reply(c, http.StatusOK, map[string]interface{}{"cancelled": true})
}

func (s *Service) finalizePendingCLIAccountSwitch(tx *gorm.DB, c *app.RequestContext, targetAgentID int64, targetSessionID string, now int64) (bool, error) {
	token := cliAccountSwitchToken(c)
	if !strings.HasPrefix(token, "efas_") {
		return false, nil
	}
	record, err := loadCLIAccountSwitch(tx, token, true)
	if err != nil {
		return false, nil
	}
	if record.Status != "pending_onboarding" || record.TargetAgentID == nil || *record.TargetAgentID != targetAgentID || record.TargetConsoleSession != targetSessionID {
		return false, nil
	}
	if record.ExpiresAt < now {
		return false, expireCLIAccountSwitch(tx, record, now)
	}
	if err := finalizeCLIAccountSwitch(tx, record, now); err != nil {
		if errors.Is(err, errConflict) {
			if updateErr := tx.Exec(`UPDATE agent_cli_account_switches SET status = 'revoked'
				WHERE switch_id_hash = ? AND status = 'pending_onboarding'`, record.SwitchIDHash).Error; updateErr != nil {
				return false, updateErr
			}
			return false, auditCLIAccountSwitch(tx, record, "rejected", now)
		}
		return false, err
	}
	return true, nil
}
