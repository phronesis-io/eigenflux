package consolev2

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const recentEmailAuthWindow = 5 * time.Minute

func requireRecentEmailAuth(c *app.RequestContext, now int64) bool {
	methodValue, methodOK := c.Get("console_auth_method")
	method, typeOK := methodValue.(string)
	recentValue, recentOK := c.Get("console_recent_auth_at")
	recent, recentTypeOK := recentValue.(int64)
	return methodOK && typeOK && method == "email_otp" && recentOK && recentTypeOK &&
		recent >= now-int64(recentEmailAuthWindow/time.Millisecond) && recent <= now
}

type createPrincipalChallengeRequest struct {
	PublicKey string `json:"public_key"`
}

func (s *Service) createPrincipalChallenge(_ context.Context, c *app.RequestContext) {
	now := time.Now().UnixMilli()
	if !requireRecentEmailAuth(c, now) {
		fail(c, http.StatusForbidden, "RECENT_EMAIL_AUTH_REQUIRED", "complete email verification again before changing device keys", nil)
		return
	}
	id, ok := agentID(c)
	sessionValue, sessionOK := c.Get("console_session_id")
	sessionID, sessionTypeOK := sessionValue.(string)
	if !ok || !sessionOK || !sessionTypeOK {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		return
	}
	var req createPrincipalChallengeRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "a valid Ed25519 public_key is required", nil)
		return
	}
	publicKey, err := decodePublicKey(req.PublicKey)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), nil)
		return
	}
	nonce, err := randomToken("efn_", 32)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "could not create device challenge", nil)
		return
	}
	expiresAt := now + int64(2*time.Minute/time.Millisecond)
	if err := s.db.Exec(`INSERT INTO agent_signature_nonces
		(nonce_hash, key_fingerprint, domain, subject_agent_id, console_session_id, expires_at, created_at)
		VALUES (?, ?, 'add_device', ?, ?, ?, ?)`, hashString(nonce), fingerprint(publicKey), id,
		sessionID, expiresAt, now).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DEVICE_CHALLENGE_FAILED", "could not create device challenge", nil)
		return
	}
	reply(c, http.StatusCreated, map[string]interface{}{
		"nonce": nonce, "issued_at": now, "expires_at": expiresAt,
		"proof_domain": "EF-AUTH-V2-ADD-DEVICE",
	})
}

type addPrincipalRequest struct {
	PublicKey string `json:"public_key"`
	Nonce     string `json:"nonce"`
	IssuedAt  int64  `json:"issued_at"`
	Signature string `json:"signature"`
}

type addPrincipalProofPayload struct {
	PublicKey string `json:"public_key"`
	Nonce     string `json:"nonce"`
	IssuedAt  int64  `json:"issued_at"`
}

func addPrincipalTranscript(req addPrincipalRequest) ([]byte, error) {
	payload, err := json.Marshal(addPrincipalProofPayload{
		PublicKey: req.PublicKey,
		Nonce:     req.Nonce,
		IssuedAt:  req.IssuedAt,
	})
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("EF-AUTH-V2-ADD-DEVICE\x00POST\n/api/v2/agents/me/principals\n%s", hashString(string(payload)))), nil
}

func (s *Service) addPrincipal(_ context.Context, c *app.RequestContext) {
	now := time.Now().UnixMilli()
	if !requireRecentEmailAuth(c, now) {
		fail(c, http.StatusForbidden, "RECENT_EMAIL_AUTH_REQUIRED", "complete email verification again before changing device keys", nil)
		return
	}
	id, ok := agentID(c)
	sessionValue, sessionOK := c.Get("console_session_id")
	sessionID, sessionTypeOK := sessionValue.(string)
	if !ok || !sessionOK || !sessionTypeOK {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		return
	}
	var req addPrincipalRequest
	if err := decodeBody(c, &req); err != nil || req.Nonce == "" || req.Signature == "" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "public_key, nonce, issued_at, and signature are required", nil)
		return
	}
	publicKey, err := decodePublicKey(req.PublicKey)
	if err != nil || req.IssuedAt < now-int64(proofClockSkew/time.Millisecond) || req.IssuedAt > now+int64(proofClockSkew/time.Millisecond) {
		fail(c, http.StatusUnauthorized, "DEVICE_PROOF_INVALID", "device proof is invalid or expired", nil)
		return
	}
	signature, sigErr := base64.RawURLEncoding.DecodeString(req.Signature)
	if sigErr != nil {
		signature, sigErr = base64.StdEncoding.DecodeString(req.Signature)
	}
	transcript, transcriptErr := addPrincipalTranscript(req)
	if sigErr != nil || transcriptErr != nil || !ed25519.Verify(publicKey, transcript, signature) {
		fail(c, http.StatusUnauthorized, "DEVICE_PROOF_INVALID", "device proof is invalid or expired", nil)
		return
	}

	accessToken, accessErr := randomToken("efv2a_", 32)
	refreshToken, refreshErr := randomToken("efv2r_", 32)
	familyID, familyErr := randomToken("eff_", 18)
	if accessErr != nil || refreshErr != nil || familyErr != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "could not issue device credentials", nil)
		return
	}
	keyFingerprint := fingerprint(publicKey)
	var principalID int64
	var scopes []string
	created := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, keyFingerprint).Error; err != nil {
			return err
		}
		nonceUse := tx.Exec(`UPDATE agent_signature_nonces SET consumed_at = ?
			WHERE nonce_hash = ? AND key_fingerprint = ? AND domain = 'add_device'
			  AND subject_agent_id = ? AND console_session_id = ?
			  AND consumed_at IS NULL AND expires_at >= ?`, now, hashString(req.Nonce), keyFingerprint,
			id, sessionID, now)
		if nonceUse.Error != nil || nonceUse.RowsAffected != 1 {
			return errUnauthorized
		}
		var existing struct {
			PrincipalID int64  `gorm:"column:principal_id"`
			AgentID     int64  `gorm:"column:agent_id"`
			Status      string `gorm:"column:status"`
		}
		if err := tx.Raw(`SELECT principal_id, agent_id, status FROM agent_principals
			WHERE key_type = 'ed25519-v1' AND key_fingerprint = ?`, keyFingerprint).Scan(&existing).Error; err != nil {
			return err
		}
		if existing.PrincipalID != 0 {
			if existing.AgentID != id || existing.Status == "revoked" || existing.Status == "suspended" {
				return errConflict
			}
			principalID = existing.PrincipalID
		} else {
			var onboardingState string
			if err := tx.Raw(`SELECT state FROM agent_onboarding_v2 WHERE agent_id = ?`, id).Scan(&onboardingState).Error; err != nil {
				return err
			}
			status := "limited"
			if onboardingState == "completed" {
				status = "active"
			}
			if err := tx.Raw(`INSERT INTO agent_principals
				(agent_id, key_type, key_fingerprint, public_key, status, created_at, last_seen_at)
				VALUES (?, 'ed25519-v1', ?, ?, ?, ?, ?) RETURNING principal_id`,
				id, keyFingerprint, []byte(publicKey), status, now, now).Scan(&principalID).Error; err != nil {
				return err
			}
			created = true
		}
		var state string
		if err := tx.Raw(`SELECT state FROM agent_onboarding_v2 WHERE agent_id = ?`, id).Scan(&state).Error; err != nil {
			return err
		}
		scopes = principalScopesForOnboarding(state)
		return tx.Exec(`INSERT INTO agent_credential_sessions
			(principal_id, family_id, access_token_hash, refresh_token_hash, audience, scopes,
			 rotation_counter, issued_at, expires_at, absolute_expires_at, last_seen_at)
			VALUES (?, ?, ?, ?, 'agent_v2', ?, 0, ?, ?, ?, ?)`, principalID, familyID,
			hashString(accessToken), hashString(refreshToken), pq.Array(scopes), now,
			now+int64(accessTTL/time.Millisecond), now+int64(refreshTTL/time.Millisecond), now).Error
	})
	if errors.Is(err, errUnauthorized) {
		fail(c, http.StatusUnauthorized, "DEVICE_PROOF_INVALID", "device challenge is invalid, consumed, or expired", nil)
		return
	}
	if errors.Is(err, errConflict) || isUniqueViolation(err) {
		fail(c, http.StatusConflict, "DEVICE_KEY_UNAVAILABLE", "this device key cannot be linked to the current Agent", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "DEVICE_LINK_FAILED", "could not link device key", nil)
		return
	}
	reply(c, http.StatusCreated, map[string]interface{}{
		"principal_id": fmt.Sprintf("%d", principalID),
		"created":      created, "access_token": accessToken, "refresh_token": refreshToken,
		"expires_at": now + int64(accessTTL/time.Millisecond), "scopes": scopes,
	})
}

func principalScopesForOnboarding(state string) []string {
	if state != "completed" {
		return []string{"onboarding:write", "context:read", "feed:read", "notifications:ack", "commands:claim", "console:handoff:create"}
	}
	return []string{
		"onboarding:write", "context:read", "feed:read", "notifications:ack", "commands:claim",
		"communication:read", "communication:write", "broadcast:write", "trade:write",
		"attention:write", "console:handoff:create",
	}
}

func (s *Service) listPrincipals(_ context.Context, c *app.RequestContext) {
	id, ok := agentID(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		return
	}
	var rows []struct {
		PrincipalID    int64  `gorm:"column:principal_id" json:"principal_id,string"`
		KeyType        string `gorm:"column:key_type" json:"key_type"`
		KeyFingerprint string `gorm:"column:key_fingerprint" json:"key_fingerprint"`
		Status         string `gorm:"column:status" json:"status"`
		CreatedAt      int64  `gorm:"column:created_at" json:"created_at"`
		LastSeenAt     int64  `gorm:"column:last_seen_at" json:"last_seen_at"`
		RevokedAt      *int64 `gorm:"column:revoked_at" json:"revoked_at,omitempty"`
	}
	if err := s.db.Raw(`SELECT principal_id, key_type, key_fingerprint, status, created_at, last_seen_at, revoked_at
		FROM agent_principals WHERE agent_id = ? ORDER BY principal_id`, id).Scan(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "PRINCIPAL_LIST_FAILED", "could not list device keys", nil)
		return
	}
	reply(c, http.StatusOK, map[string]interface{}{"principals": rows})
}

func (s *Service) revokePrincipal(_ context.Context, c *app.RequestContext) {
	now := time.Now().UnixMilli()
	if !requireRecentEmailAuth(c, now) {
		fail(c, http.StatusForbidden, "RECENT_EMAIL_AUTH_REQUIRED", "complete email verification again before changing device keys", nil)
		return
	}
	id, ok := agentID(c)
	principalID, err := strconv.ParseInt(c.Param("principal_id"), 10, 64)
	if !ok || err != nil || principalID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "a valid principal_id is required", nil)
		return
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var keyType, status string
		if err := tx.Raw(`SELECT key_type, status FROM agent_principals
			WHERE principal_id = ? AND agent_id = ? FOR UPDATE`, principalID, id).Row().Scan(&keyType, &status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errUnauthorized
			}
			return err
		}
		if keyType != "ed25519-v1" {
			return errConflict
		}
		if status == "revoked" {
			return nil
		}
		if err := tx.Exec(`UPDATE agent_principals SET status = 'revoked', revoked_at = ?
			WHERE principal_id = ? AND agent_id = ?`, now, principalID, id).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE agent_credential_sessions SET revoked_at = COALESCE(revoked_at, ?)
			WHERE principal_id = ?`, now, principalID).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE console_v2_sessions SET status = 'revoked', revoked_at = COALESCE(revoked_at, ?)
			WHERE principal_id = ? AND status = 'active'`, now, principalID).Error
	})
	if errors.Is(err, errUnauthorized) {
		fail(c, http.StatusNotFound, "PRINCIPAL_NOT_FOUND", "device key was not found", nil)
		return
	}
	if errors.Is(err, errConflict) {
		fail(c, http.StatusConflict, "PRINCIPAL_NOT_REVOCABLE", "the email recovery anchor is not a device key", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "PRINCIPAL_REVOKE_FAILED", "could not revoke device key", nil)
		return
	}
	reply(c, http.StatusOK, map[string]interface{}{"principal_id": fmt.Sprintf("%d", principalID), "revoked": true})
}
