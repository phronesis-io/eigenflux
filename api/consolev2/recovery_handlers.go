package consolev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"eigenflux_server/pkg/agentidentity"
	mailservice "eigenflux_server/pkg/email"
)

const accountRecoveryTTL = 5 * time.Minute

const (
	recoverySourceAbandon  = "abandon"
	recoverySourcePreserve = "preserve"
)

func recoverySourceDisposition(activeBindingID int64) string {
	if activeBindingID > 0 {
		return recoverySourcePreserve
	}
	return recoverySourceAbandon
}

type recoveryCandidate struct {
	DisplayName  string `json:"display_name"`
	EigenFluxID  string `json:"eigenflux_id"`
	JoinedAt     int64  `json:"joined_at"`
	LastActiveAt int64  `json:"last_active_at"`
}

func eigenFluxID(shortID string) string {
	return "eigenflux#" + strings.TrimSpace(shortID)
}

func (s *Service) prepareAccountRecovery(tx *gorm.DB, sourceAgentID, targetAgentID, principalID int64, sessionID, challengeID, normalizedEmail string, now int64) (map[string]interface{}, error) {
	var session struct {
		Capabilities pq.StringArray `gorm:"column:client_capabilities;type:text[]"`
	}
	if err := tx.Raw(`SELECT client_capabilities FROM console_v2_sessions
		WHERE session_id = ? AND agent_id = ? AND principal_id = ? AND status = 'active'
		FOR UPDATE`, sessionID, sourceAgentID, principalID).Scan(&session).Error; err != nil {
		return nil, err
	}
	if !containsScope(session.Capabilities, "account_recovery_v1") {
		return nil, errConflict
	}
	var candidate struct {
		AgentName     string `gorm:"column:agent_name"`
		ShortID       string `gorm:"column:short_id"`
		IdentityState string `gorm:"column:identity_state"`
		CreatedAt     int64  `gorm:"column:created_at"`
		LastActiveAt  int64  `gorm:"column:last_active_at"`
	}
	if err := tx.Raw(`SELECT agent.agent_name, agent.short_id, agent.identity_state, agent.created_at,
		CASE WHEN jsonb_typeof(card.public_card->'last_active_at') = 'number'
			THEN (card.public_card->>'last_active_at')::bigint
			ELSE agent.updated_at END AS last_active_at
		FROM agents agent LEFT JOIN agent_cards card ON card.agent_id = agent.agent_id
		WHERE agent.agent_id = ? FOR UPDATE OF agent`, targetAgentID).Scan(&candidate).Error; err != nil {
		return nil, err
	}
	if candidate.IdentityState != "active" {
		return nil, errConflict
	}
	var suspended int64
	if err := tx.Raw(`SELECT COUNT(*) FROM agent_principals
		WHERE agent_id = ? AND status = 'suspended'`, targetAgentID).Scan(&suspended).Error; err != nil {
		return nil, err
	}
	if suspended > 0 {
		return nil, errConflict
	}
	var sourceBindingID int64
	if err := tx.Raw(`SELECT binding_id FROM agent_email_bindings
		WHERE agent_id = ? AND status = 'active' LIMIT 1 FOR UPDATE`, sourceAgentID).Scan(&sourceBindingID).Error; err != nil {
		return nil, err
	}
	sourceDisposition := recoverySourceDisposition(sourceBindingID)
	recoveryID, err := randomToken("efar_", 32)
	if err != nil {
		return nil, err
	}
	if err := tx.Exec(`UPDATE agent_account_recoveries SET status = 'revoked'
		WHERE source_agent_id = ? AND console_session_id = ? AND status = 'pending'`, sourceAgentID, sessionID).Error; err != nil {
		return nil, err
	}
	if err := tx.Exec(`INSERT INTO agent_account_recoveries
		(recovery_id_hash, source_agent_id, target_agent_id, principal_id, console_session_id,
		 email_challenge_id, normalized_email_hash, source_disposition, status, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, hashString(recoveryID), sourceAgentID,
		targetAgentID, principalID, sessionID, challengeID, keyedHash(s.otpPepper, normalizedEmail),
		sourceDisposition, now+int64(accountRecoveryTTL/time.Millisecond), now).Error; err != nil {
		return nil, err
	}
	consume := tx.Exec(`UPDATE v2_email_challenges SET status = 'consumed', consumed_at = ?
		WHERE challenge_id = ? AND status = 'pending'`, now, challengeID)
	if consume.Error != nil || consume.RowsAffected != 1 {
		return nil, errUnauthorized
	}
	return map[string]interface{}{
		"reason":             "existing_agent_recovery_available",
		"recovery_id":        recoveryID,
		"source_disposition": sourceDisposition,
		"candidate": recoveryCandidate{
			DisplayName: agentidentity.DisplayName(candidate.AgentName, candidate.ShortID), EigenFluxID: eigenFluxID(candidate.ShortID),
			JoinedAt: candidate.CreatedAt, LastActiveAt: candidate.LastActiveAt,
		},
	}, nil
}

type recoveryRecord struct {
	SourceAgentID       int64           `gorm:"column:source_agent_id"`
	TargetAgentID       int64           `gorm:"column:target_agent_id"`
	PrincipalID         int64           `gorm:"column:principal_id"`
	ConsoleSessionID    string          `gorm:"column:console_session_id"`
	EmailChallengeID    string          `gorm:"column:email_challenge_id"`
	NormalizedEmailHash string          `gorm:"column:normalized_email_hash"`
	SourceDisposition   string          `gorm:"column:source_disposition"`
	Status              string          `gorm:"column:status"`
	ExpiresAt           int64           `gorm:"column:expires_at"`
	ResultSnapshot      json.RawMessage `gorm:"column:result_snapshot;type:jsonb"`
}

func (s *Service) confirmAccountRecovery(_ context.Context, c *app.RequestContext) {
	sourceAgentID, ok := agentID(c)
	principalValue, principalOK := c.Get("principal_id")
	principalID, principalTypeOK := principalValue.(int64)
	sessionValue, sessionOK := c.Get("console_session_id")
	sessionID, sessionTypeOK := sessionValue.(string)
	recoveryID := strings.TrimSpace(c.Param("recovery_id"))
	if !ok || !principalOK || !principalTypeOK || !sessionOK || !sessionTypeOK || !strings.HasPrefix(recoveryID, "efar_") {
		fail(c, http.StatusBadRequest, "RECOVERY_INVALID", "account recovery is invalid or expired", nil)
		return
	}

	var response map[string]interface{}
	var notificationEmail, notificationAgentName string
	recoveryExpired := false
	now := time.Now().UnixMilli()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var recovery recoveryRecord
		if err := tx.Raw(`SELECT source_agent_id, target_agent_id, principal_id, console_session_id,
			email_challenge_id, normalized_email_hash, source_disposition, status, expires_at, result_snapshot
			FROM agent_account_recoveries WHERE recovery_id_hash = ? FOR UPDATE`, hashString(recoveryID)).
			Scan(&recovery).Error; err != nil {
			return err
		}
		if recovery.SourceAgentID == 0 || (recovery.SourceAgentID != sourceAgentID && recovery.TargetAgentID != sourceAgentID) ||
			recovery.PrincipalID != principalID || recovery.ConsoleSessionID != sessionID {
			return errUnauthorized
		}
		if recovery.Status == "completed" {
			return json.Unmarshal(recovery.ResultSnapshot, &response)
		}
		if recovery.Status == "pending" && recovery.ExpiresAt < now {
			if err := tx.Exec(`UPDATE agent_account_recoveries SET status = 'expired' WHERE recovery_id_hash = ?`, hashString(recoveryID)).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO agent_account_recovery_audit
					(recovery_id_hash, source_agent_id, target_agent_id, principal_id, console_session_id, result, occurred_at)
					VALUES (?, ?, ?, ?, ?, 'expired', ?)`, hashString(recoveryID), recovery.SourceAgentID,
				recovery.TargetAgentID, recovery.PrincipalID, recovery.ConsoleSessionID, now).Error; err != nil {
				return err
			}
			recoveryExpired = true
			return nil
		}
		if recovery.Status != "pending" {
			return errUnauthorized
		}

		var session struct {
			AgentID      int64          `gorm:"column:agent_id"`
			PrincipalID  int64          `gorm:"column:principal_id"`
			Capabilities pq.StringArray `gorm:"column:client_capabilities;type:text[]"`
		}
		if err := tx.Raw(`SELECT agent_id, principal_id, client_capabilities FROM console_v2_sessions
			WHERE session_id = ? AND status = 'active' FOR UPDATE`, sessionID).Scan(&session).Error; err != nil {
			return err
		}
		if session.AgentID != recovery.SourceAgentID || session.PrincipalID != recovery.PrincipalID ||
			!containsScope(session.Capabilities, "account_recovery_v1") {
			return errUnauthorized
		}

		var sourceState, targetState string
		if err := tx.Raw(`SELECT identity_state FROM agents WHERE agent_id = ? FOR UPDATE`, recovery.SourceAgentID).Scan(&sourceState).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT identity_state FROM agents WHERE agent_id = ? FOR UPDATE`, recovery.TargetAgentID).Scan(&targetState).Error; err != nil {
			return err
		}
		if sourceState != "active" || targetState != "active" {
			return errConflict
		}
		var sourceBindingID int64
		if err := tx.Raw(`SELECT binding_id FROM agent_email_bindings
			WHERE agent_id = ? AND status = 'active' LIMIT 1 FOR UPDATE`, recovery.SourceAgentID).Scan(&sourceBindingID).Error; err != nil {
			return err
		}
		sourceDisposition := recoverySourceDisposition(sourceBindingID)
		if sourceDisposition != recovery.SourceDisposition {
			return errConflict
		}

		var principal struct {
			AgentID int64  `gorm:"column:agent_id"`
			KeyType string `gorm:"column:key_type"`
			Status  string `gorm:"column:status"`
		}
		if err := tx.Raw(`SELECT agent_id, key_type, status FROM agent_principals
			WHERE principal_id = ? FOR UPDATE`, recovery.PrincipalID).Scan(&principal).Error; err != nil {
			return err
		}
		if principal.AgentID != recovery.SourceAgentID || principal.KeyType != "ed25519-v1" ||
			(principal.Status != "limited" && principal.Status != "active") {
			return errConflict
		}

		var binding struct {
			BindingID         int64  `gorm:"column:binding_id"`
			NormalizedEmail   string `gorm:"column:normalized_email"`
			VerificationState string `gorm:"column:verification_state"`
		}
		if err := tx.Raw(`SELECT binding_id, normalized_email, verification_state FROM agent_email_bindings
			WHERE agent_id = ? AND status = 'active' FOR UPDATE`, recovery.TargetAgentID).Scan(&binding).Error; err != nil {
			return err
		}
		if binding.BindingID == 0 || keyedHash(s.otpPepper, binding.NormalizedEmail) != recovery.NormalizedEmailHash {
			return errConflict
		}
		var emailOwnerCount, suspendedCount int64
		if err := tx.Raw(`SELECT COUNT(*) FROM (
			SELECT DISTINCT agent_id FROM (
				SELECT agent_id FROM agent_email_bindings WHERE normalized_email = ? AND status = 'active'
				UNION ALL
				SELECT agent_id FROM agents WHERE lower(btrim(email)) = ? AND email_kind = 'legacy_real'
			) candidates
		) owners`, binding.NormalizedEmail, binding.NormalizedEmail).Scan(&emailOwnerCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM agent_principals WHERE agent_id = ? AND status = 'suspended'`, recovery.TargetAgentID).Scan(&suspendedCount).Error; err != nil {
			return err
		}
		if emailOwnerCount != 1 || suspendedCount != 0 {
			return errConflict
		}
		var credentialSessionIDs []int64
		if err := tx.Raw(`SELECT session_id FROM agent_credential_sessions
			WHERE principal_id = ? AND revoked_at IS NULL FOR UPDATE`, recovery.PrincipalID).Scan(&credentialSessionIDs).Error; err != nil {
			return err
		}
		if len(credentialSessionIDs) == 0 {
			return errConflict
		}
		if err := ensureLegacyConsoleV2State(tx, recovery.TargetAgentID, now); err != nil {
			return err
		}
		var targetOnboarding struct {
			State       string `gorm:"column:state"`
			CurrentStep int16  `gorm:"column:current_step"`
			Revision    int64  `gorm:"column:revision"`
		}
		if err := tx.Raw(`SELECT state, current_step, revision FROM agent_onboarding_v2
			WHERE agent_id = ? FOR UPDATE`, recovery.TargetAgentID).Scan(&targetOnboarding).Error; err != nil {
			return err
		}
		scopes := principalScopesForOnboarding(targetOnboarding.State)
		principalStatus := "limited"
		if targetOnboarding.State == "completed" {
			principalStatus = "active"
		}
		if err := tx.Exec(`UPDATE agent_principals SET agent_id = ?, status = ?, last_seen_at = ?
			WHERE principal_id = ? AND agent_id = ?`, recovery.TargetAgentID, principalStatus, now,
			recovery.PrincipalID, recovery.SourceAgentID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE agent_credential_sessions SET scopes = ?, access_refresh_required = TRUE
			WHERE principal_id = ? AND revoked_at IS NULL`, pq.Array(scopes), recovery.PrincipalID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE console_v2_sessions SET agent_id = ?, scopes = ?, last_seen_at = ?
			WHERE session_id = ? AND agent_id = ?`, recovery.TargetAgentID,
			pq.Array([]string{"console:onboarding", "console:read", "console:write"}), now,
			sessionID, recovery.SourceAgentID).Error; err != nil {
			return err
		}
		if sourceDisposition == recoverySourceAbandon {
			if err := tx.Exec(`UPDATE agent_credential_sessions sessions SET revoked_at = COALESCE(sessions.revoked_at, ?)
				FROM agent_principals principals
				WHERE sessions.principal_id = principals.principal_id AND principals.agent_id = ?
				  AND sessions.revoked_at IS NULL`, now, recovery.SourceAgentID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE agent_principals SET status = 'revoked', revoked_at = COALESCE(revoked_at, ?)
				WHERE agent_id = ? AND revoked_at IS NULL`, now, recovery.SourceAgentID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE console_v2_sessions SET status = 'revoked', revoked_at = ?
				WHERE agent_id = ? AND session_id <> ? AND status = 'active'`, now, recovery.SourceAgentID, sessionID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE console_v2_handoffs SET revoked_at = COALESCE(revoked_at, ?)
				WHERE agent_id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, now, recovery.SourceAgentID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE agent_email_bindings
				SET status = 'revoked', verification_state = 'revoked', revoked_at = COALESCE(revoked_at, ?), updated_at = ?
				WHERE agent_id = ? AND status = 'active'`, now, now, recovery.SourceAgentID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE agents SET identity_state = 'recovered_temporary',
				email = ?, email_kind = 'internal_alias', email_verified_at = NULL, updated_at = ?
				WHERE agent_id = ? AND identity_state = 'active'`,
				fmt.Sprintf("recovered-%d@identity.invalid", recovery.SourceAgentID), now, recovery.SourceAgentID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`DELETE FROM agent_onboarding_drafts WHERE agent_id = ?`, recovery.SourceAgentID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`DELETE FROM agent_cards WHERE agent_id = ?`, recovery.SourceAgentID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`DELETE FROM agent_network_memberships WHERE agent_id = ?`, recovery.SourceAgentID).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Exec(`UPDATE console_v2_sessions SET status = 'revoked', revoked_at = ?
				WHERE principal_id = ? AND session_id <> ? AND status = 'active'`, now, recovery.PrincipalID, sessionID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE console_v2_handoffs SET revoked_at = COALESCE(revoked_at, ?)
				WHERE agent_id = ? AND principal_id = ? AND consumed_at IS NULL AND revoked_at IS NULL`,
				now, recovery.SourceAgentID, recovery.PrincipalID).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`UPDATE agent_email_bindings SET verification_state = 'verified',
			verified_at = COALESCE(verified_at, ?), updated_at = ? WHERE binding_id = ?`, now, now, binding.BindingID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE agents SET email_verified_at = COALESCE(email_verified_at, ?), updated_at = ?
			WHERE agent_id = ?`, now, now, recovery.TargetAgentID).Error; err != nil {
			return err
		}
		response = map[string]interface{}{
			"recovered":              true,
			"source_disposition":     sourceDisposition,
			"source_agent_abandoned": sourceDisposition == recoverySourceAbandon,
			"agent_id":               fmt.Sprintf("%d", recovery.TargetAgentID),
			"principal_id":           fmt.Sprintf("%d", recovery.PrincipalID),
			"scopes":                 scopes,
			"onboarding": map[string]interface{}{
				"state": targetOnboarding.State, "current_step": targetOnboarding.CurrentStep,
				"revision": targetOnboarding.Revision,
			},
		}
		snapshot, err := json.Marshal(response)
		if err != nil {
			return err
		}
		complete := tx.Exec(`UPDATE agent_account_recoveries
			SET status = 'completed', completed_at = ?, result_snapshot = ?::jsonb
			WHERE recovery_id_hash = ? AND status = 'pending'`, now, string(snapshot), hashString(recoveryID))
		if complete.Error != nil || complete.RowsAffected != 1 {
			return errConflict
		}
		if err := tx.Exec(`INSERT INTO agent_account_recovery_audit
			(recovery_id_hash, source_agent_id, target_agent_id, principal_id, console_session_id, result, occurred_at)
			VALUES (?, ?, ?, ?, ?, 'completed', ?)`, hashString(recoveryID), recovery.SourceAgentID,
			recovery.TargetAgentID, recovery.PrincipalID, recovery.ConsoleSessionID, now).Error; err != nil {
			return err
		}
		notificationEmail = binding.NormalizedEmail
		_ = tx.Raw(`SELECT agent_name FROM agents WHERE agent_id = ?`, recovery.TargetAgentID).Scan(&notificationAgentName).Error
		return nil
	})
	if recoveryExpired || errors.Is(err, errUnauthorized) {
		fail(c, http.StatusUnauthorized, "RECOVERY_EXPIRED", "account recovery expired; verify the email again", nil)
		return
	}
	if errors.Is(err, errConflict) || isUniqueViolation(err) {
		fail(c, http.StatusConflict, "RECOVERY_NOT_ALLOWED", "this Agent cannot be recovered automatically", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "RECOVERY_FAILED", "could not recover the historical Agent", nil)
		return
	}
	if notifier, ok := s.emailSender.(mailservice.AccountRecoveryNotifier); ok && notificationEmail != "" {
		go func() {
			notifyContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = notifier.SendAccountRecoveryMail(notifyContext, notificationEmail, notificationAgentName)
		}()
	}
	reply(c, http.StatusOK, response)
}
