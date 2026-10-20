package consolev2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

const existingIdentityRecoveryChannel = "existing_recovery"

type existingIdentityRecoveryChallengeRequest struct {
	PublicKey       string `json:"public_key"`
	IdempotencyKey  string `json:"idempotency_key"`
	ExpectedAgentID string `json:"expected_agent_id"`
}

// Existing keys can reauthenticate while public creation of new identities is
// disabled. The subject-bound grant cannot create an Agent or a device key.
func (s *Service) issueExistingIdentityRecoveryChallenge(ctx context.Context, c *app.RequestContext) {
	failure := refreshFailure{authenticationFailure: authenticationFailure{ctx: ctx, stage: "recovery_challenge"}, c: c}
	var req existingIdentityRecoveryChallengeRequest
	if err := decodeBody(c, &req); err != nil {
		failure.reject(http.StatusBadRequest, "INVALID_REQUEST", "malformed_request", "invalid recovery challenge request", nil, nil)
		return
	}
	publicKey, keyErr := decodePublicKey(req.PublicKey)
	agentID, idErr := strconv.ParseInt(req.ExpectedAgentID, 10, 64)
	if keyErr != nil || idErr != nil || agentID <= 0 || len(req.IdempotencyKey) < 16 || len(req.IdempotencyKey) > 128 {
		failure.reject(http.StatusBadRequest, "INVALID_REQUEST", "malformed_request", "public_key, idempotency_key, and a positive expected_agent_id are required", nil, nil)
		return
	}
	keyFingerprint := fingerprint(publicKey)
	clientIP := resolveV2ClientIP(c.RemoteAddr().String(),
		string(c.GetHeader("X-Forwarded-For")), string(c.GetHeader("X-Real-IP")), s.trustedProxyNets)
	rateContext, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	decision, rateErr := s.allowPublicRegistration(rateContext, clientIP, keyFingerprint)
	cancel()
	if rateErr != nil {
		failure.unavailable("recovery_limiter_failed", rateErr)
		return
	}
	if !decision.Allowed {
		failure.reject(http.StatusTooManyRequests, "RECOVERY_RATE_LIMITED", "recovery_rate_limited", "Agent authentication recovery is temporarily rate limited", nil,
			map[string]interface{}{"retry_after_ms": decision.RetryAfterMS})
		return
	}
	var principalID int64
	db := s.authenticationDB(ctx)
	if err := db.Raw(`SELECT p.principal_id FROM agent_principals p
		JOIN agents a ON a.agent_id = p.agent_id
		WHERE p.key_type = 'ed25519-v1' AND p.key_fingerprint = ? AND p.agent_id = ?
		  AND p.status IN ('active', 'limited') AND p.revoked_at IS NULL AND a.identity_state = 'active'`,
		keyFingerprint, agentID).Scan(&principalID).Error; err != nil {
		failure.unavailable("recovery_identity_lookup_failed", err)
		return
	}
	if principalID == 0 {
		failure.reject(http.StatusUnauthorized, "RECOVERY_IDENTITY_INVALID", "identity_binding_invalid", "existing Agent identity is unavailable for authentication recovery", nil, nil)
		return
	}
	failure.agentID, failure.principalID = agentID, principalID
	result, err := s.issueBootstrapGrantRecordWithDB(db, issueGrantRequest{
		EntitlementID:  "existing-identity-recovery:" + keyFingerprint + ":" + req.ExpectedAgentID + ":" + req.IdempotencyKey,
		IdempotencyKey: req.IdempotencyKey, Channel: existingIdentityRecoveryChannel, Policy: "limited",
		PublicKey: req.PublicKey, SubjectAgentID: &agentID,
	}, publicKey)
	if errors.Is(err, errConflict) {
		failure.reject(http.StatusConflict, "RECOVERY_IDEMPOTENCY_CONFLICT", "recovery_request_conflict", "recovery request key was reused with different input", nil, nil)
		return
	}
	if err != nil {
		failure.unavailable("recovery_grant_failed", err)
		return
	}
	reply(c, http.StatusCreated, result.response())
}
