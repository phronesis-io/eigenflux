package consolev2

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"eigenflux_server/pkg/logger"
)

// The switch cookie identifies the initiating CLI principal. The active browser
// account is deliberately not used to choose which CLI identity is moved.
func (s *Service) pendingEmailSwitch(tx *gorm.DB, c *app.RequestContext, now int64) (cliAccountSwitchRecord, error) {
	record, err := loadCLIAccountSwitch(tx, cliAccountSwitchToken(c), true)
	if err != nil {
		return record, err
	}
	if record.Status != "pending_target" || record.ExpiresAt < now {
		return record, errUnauthorized
	}
	if err := validateNoopPrincipalBinding(tx, record); err != nil {
		return record, err
	}
	// A switch-specific handoff session must still be held by this browser.
	for slot := 0; slot < maxConsoleAccountSlots; slot++ {
		credential, ok := consoleCredential(c, slot)
		if !ok {
			continue
		}
		session, loadErr := s.loadConsoleSessionCredential(tx, credential)
		if loadErr == nil && session.SessionID == record.SourceConsoleSession && validConsoleSessionAt(session, now) {
			return record, nil
		}
	}
	return record, errUnauthorized
}

func (s *Service) createCLIAccountSwitchChallenge(ctx context.Context, c *app.RequestContext) {
	var req createEmailChallengeRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, 400, "INVALID_REQUEST", "a valid email is required", nil)
		return
	}
	email, err := normalizeV2Email(req.Email)
	if err != nil {
		fail(c, 400, "INVALID_REQUEST", "a valid email is required", nil)
		return
	}
	now := time.Now().UnixMilli()
	var job emailJob
	var expiresAt int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		record, err := s.pendingEmailSwitch(tx, c, now)
		if err != nil {
			return err
		}
		checked, err := s.preflightEmailChallengeRate(ctx, email, s.clientIPHash(c), &record.SourceConsoleSession)
		if err != nil {
			return err
		}
		// Recovery-purpose challenges with a source session cannot be consumed by
		// either public login (no session) or initial binding (bind purpose).
		job, expiresAt, err = s.insertEmailChallenge(tx, email, "recovery", &record.SourceAgentID, &record.SourceConsoleSession, s.clientIPHash(c), now, checked)
		return err
	})
	if err != nil {
		s.emailSwitchFailure(ctx, c, err)
		return
	}
	s.queueEmailChallenge(c, job, expiresAt)
}

func (s *Service) emailSwitchFailure(ctx context.Context, c *app.RequestContext, err error) {
	logger.Ctx(ctx).Warn("console_account_switch_rejected", "error", err)
	switch {
	case errors.Is(err, errRateLimited):
		fail(c, 429, "EMAIL_RATE_LIMITED", "too many verification attempts; try again later", nil)
	case errors.Is(err, errInvalidOTP):
		fail(c, 401, "OTP_INVALID", "verification code is invalid or expired", nil)
	case errors.Is(err, errUnauthorized):
		fail(c, 401, "ACCOUNT_SWITCH_INVALID", "CLI account switch is invalid or expired", nil)
	case errors.Is(err, errConflict), isUniqueViolation(err):
		fail(c, 409, "ACCOUNT_SWITCH_CONFLICT", "the selected account cannot be used for this CLI switch", nil)
	default:
		fail(c, 500, "ACCOUNT_SWITCH_FAILED", "could not switch the CLI account", nil)
	}
}

// resolveSwitchEmail runs only after OTP proof, under the email ownership lock.
func (s *Service) resolveSwitchEmail(tx *gorm.DB, email string, now int64) (int64, bool, error) {
	var owners []int64
	if err := tx.Raw(`SELECT agent_id FROM agent_email_bindings WHERE normalized_email = ? AND status = 'active'
		UNION SELECT agent_id FROM agents WHERE email_kind = 'legacy_real' AND lower(btrim(email)) = ?`, email, email).Scan(&owners).Error; err != nil {
		return 0, false, err
	}
	if len(owners) > 1 {
		return 0, false, errConflict
	}
	created := len(owners) == 0
	var id int64
	if created {
		var err error
		id, err = s.idgen.NextID()
		if err != nil {
			return 0, false, err
		}
		if err = insertProvisionedAgent(tx, id, email, "EigenFlux Agent", now); err != nil {
			return 0, false, err
		}
		if err = tx.Exec(`INSERT INTO agent_profiles (agent_id, status, updated_at) VALUES (?, 0, ?)`, id, now).Error; err != nil {
			return 0, false, err
		}
	} else {
		id = owners[0]
	}
	var identity string
	if err := tx.Raw(`SELECT identity_state FROM agents WHERE agent_id = ? FOR UPDATE`, id).Scan(&identity).Error; err != nil {
		return 0, false, err
	}
	if identity != "active" {
		return 0, false, errConflict
	}
	var suspended int64
	if err := tx.Raw(`SELECT COUNT(*) FROM agent_principals WHERE agent_id = ? AND status = 'suspended'`, id).Scan(&suspended).Error; err != nil {
		return 0, false, err
	}
	if suspended > 0 {
		return 0, false, errConflict
	}
	if err := tx.Exec(`INSERT INTO agent_email_bindings
		(agent_id, normalized_email, normalization_version, verification_state, status, verified_at, created_at, updated_at)
		SELECT ?, ?, 1, 'verified', 'active', ?, ?, ? WHERE NOT EXISTS
		(SELECT 1 FROM agent_email_bindings WHERE agent_id = ? AND status = 'active')`, id, email, now, now, now, id).Error; err != nil {
		return 0, false, err
	}
	res := tx.Exec(`UPDATE agent_email_bindings SET verification_state = 'verified', verified_at = ?, updated_at = ?
		WHERE agent_id = ? AND normalized_email = ? AND status = 'active'`, now, now, id, email)
	if res.Error != nil {
		return 0, false, res.Error
	}
	if res.RowsAffected != 1 {
		return 0, false, errConflict
	}
	if err := tx.Exec(`UPDATE agents SET email = ?, email_kind = CASE WHEN email_kind = 'legacy_real' THEN 'legacy_real' ELSE 'v2_bound' END,
		email_verified_at = ?, updated_at = ? WHERE agent_id = ?`, email, now, now, id).Error; err != nil {
		return 0, false, err
	}
	if err := ensureLegacyConsoleV2State(tx, id, now); err != nil {
		return 0, false, err
	}
	if created {
		if err := tx.Exec(`UPDATE agent_onboarding_v2 SET state = 'in_progress' WHERE agent_id = ?`, id).Error; err != nil {
			return 0, false, err
		}
	}
	return id, created, nil
}

func (s *Service) verifyCLIAccountSwitchEmail(ctx context.Context, c *app.RequestContext) {
	var req verifyEmailRequest
	if err := decodeBody(c, &req); err != nil || req.ChallengeID == "" || req.OTP == "" {
		fail(c, 400, "INVALID_REQUEST", "challenge_id, email and otp are required", nil)
		return
	}
	email, err := normalizeV2Email(req.Email)
	if err != nil {
		fail(c, 400, "INVALID_REQUEST", "a valid email is required", nil)
		return
	}
	now := time.Now().UnixMilli()
	sessionID, err := randomToken("efcs_", 18)
	if err != nil {
		s.emailSwitchFailure(ctx, c, err)
		return
	}
	secret, err := randomToken("", 32)
	if err != nil {
		s.emailSwitchFailure(ctx, c, err)
		return
	}
	csrf, err := randomToken("efcsrf_", 24)
	if err != nil {
		s.emailSwitchFailure(ctx, c, err)
		return
	}
	var targetID int64
	var slot int
	var noop, requiresOnboarding, valid bool
	status := "completed"
	err = s.db.Transaction(func(tx *gorm.DB) error {
		record, err := s.pendingEmailSwitch(tx, c, now)
		if err != nil {
			return err
		}
		_, valid, err = s.lockAndCheckEmailChallenge(tx, req, email, "recovery", &record.SourceAgentID, &record.SourceConsoleSession, now)
		if err != nil {
			if errors.Is(err, errUnauthorized) {
				return errInvalidOTP
			}
			return err
		}
		if !valid {
			return nil
		} // Commit failed-attempt accounting.
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, keyedHash(s.otpPepper, email)).Error; err != nil {
			return err
		}
		target, created, err := s.resolveSwitchEmail(tx, email, now)
		if err != nil {
			return err
		}
		targetID = target
		var onboarding string
		if err := tx.Raw(`SELECT state FROM agent_onboarding_v2 WHERE agent_id = ?`, targetID).Scan(&onboarding).Error; err != nil {
			return err
		}
		requiresOnboarding = onboarding != "completed"
		// Reuse the source handoff slot when all browser slots are occupied.
		// This operation changes this Agent's login, not another browser account.
		var replaced string
		slot, replaced, _, err = s.chooseConsoleSessionSlot(tx, c, targetID, 0, now)
		if errors.Is(err, errConsoleAccountLimit) {
			slot, replaced, _, err = s.chooseConsoleSessionSlot(tx, c, targetID, record.SourceAgentID, now)
		}
		if err != nil {
			return err
		}
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return err
		}
		var browserPrincipal int64
		if err := tx.Raw(`INSERT INTO agent_principals (agent_id, key_type, key_fingerprint, public_key, status, created_at, last_seen_at)
			VALUES (?, 'email-recovery-v1', ?, ?, 'limited', ?, ?) RETURNING principal_id`, targetID, fingerprintForKeyType("email-recovery-v1", key), key, now, now).Scan(&browserPrincipal).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO console_v2_sessions
			(session_id, session_secret_hash, agent_id, principal_id, csrf_secret_hash, status, scopes, issued_at, idle_expires_at, absolute_expires_at, last_seen_at, auth_method, recent_auth_at)
			VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, 'email_otp', ?)`, sessionID, hashString(secret), targetID, browserPrincipal, hashString(csrf),
			pq.Array([]string{"console:onboarding", "console:read", "console:write"}), now, now+int64(consoleIdleTTL/time.Millisecond), now+int64(consoleAbsoluteTTL/time.Millisecond), now, now).Error; err != nil {
			return err
		}
		if replaced != "" {
			if err := tx.Exec(`UPDATE console_v2_sessions SET status = 'revoked', revoked_at = ? WHERE session_id = ?`, now, replaced).Error; err != nil {
				return err
			}
		}
		noop = targetID == record.SourceAgentID
		if noop {
			requiresOnboarding = false
			if err := completeNoopCLIAccountSwitch(tx, record, now); err != nil {
				return err
			}
		} else {
			record.TargetAgentID, record.TargetConsoleSession, record.OwnershipVerifiedAt = &targetID, sessionID, &now
			if created || !requiresOnboarding {
				if err := finalizeCLIAccountSwitch(tx, record, now, created); err != nil {
					return err
				}
			} else {
				status = "pending_onboarding"
				if err := tx.Exec(`UPDATE agent_cli_account_switches SET target_agent_id = ?, target_console_session_id = ?, ownership_verified_at = ?, status = 'pending_onboarding' WHERE switch_id_hash = ?`, targetID, sessionID, now, record.SwitchIDHash).Error; err != nil {
					return err
				}
				if err := auditCLIAccountSwitch(tx, record, status, now); err != nil {
					return err
				}
			}
		}
		return tx.Exec(`UPDATE v2_email_challenges SET status = 'consumed', consumed_at = ? WHERE challenge_id = ?`, now, req.ChallengeID).Error
	})
	if err != nil {
		s.emailSwitchFailure(ctx, c, err)
		return
	}
	if !valid {
		s.emailSwitchFailure(ctx, c, errInvalidOTP)
		return
	}
	s.setConsoleCookieAtSlot(c, slot, sessionID+"."+secret, int(consoleAbsoluteTTL/time.Second))
	s.setCSRFCookieAtSlot(c, slot, csrf, int(consoleAbsoluteTTL/time.Second))
	s.setActiveConsoleSlot(c, slot, int(consoleAbsoluteTTL/time.Second))
	// Retain the opaque record cookie so a refreshed success page can recover
	// its result and the optional onboarding continuation from server state.
	reply(c, http.StatusOK, map[string]interface{}{"status": status, "agent_id": fmt.Sprint(targetID), "already_current": noop,
		"refresh_required": status == "completed" && !noop, "requires_onboarding": requiresOnboarding, "csrf_token": csrf})
}
