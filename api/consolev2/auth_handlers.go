package consolev2

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	"gorm.io/gorm"

	consoledal "eigenflux_server/api/dal"
	"eigenflux_server/pkg/activity"
	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/runtimeidentity"
)

type issueGrantRequest struct {
	EntitlementID  string `json:"entitlement_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Channel        string `json:"channel"`
	Policy         string `json:"policy"`
	PublicKey      string `json:"public_key"`
}

type bootstrapGrantResult struct {
	BootstrapGrant string
	Nonce          string
	ExpiresAt      int64
	KeyFingerprint string
}

func (result bootstrapGrantResult) response() map[string]interface{} {
	return map[string]interface{}{
		"bootstrap_grant": result.BootstrapGrant,
		"nonce":           result.Nonce,
		"expires_at":      result.ExpiresAt,
		"key_fingerprint": result.KeyFingerprint,
		"proof": map[string]interface{}{
			"algorithm": "ed25519",
			"domain":    "EF-AUTH-V2",
			"body":      "sign the canonical provision payload described by the API contract",
		},
	}
}

func (s *Service) issueBootstrapGrant(_ context.Context, c *app.RequestContext) {
	if s.bootstrapSecret == "" || subtleHeaderMismatch(string(c.GetHeader("X-Bootstrap-Broker-Secret")), s.bootstrapSecret) {
		fail(c, http.StatusNotFound, "NOT_FOUND", "resource not found", nil)
		return
	}
	var req issueGrantRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), nil)
		return
	}
	publicKey, err := decodePublicKey(req.PublicKey)
	if err != nil || strings.TrimSpace(req.EntitlementID) == "" || len(req.IdempotencyKey) < 16 || len(req.IdempotencyKey) > 128 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "entitlement_id, idempotency_key, and a valid public_key are required", nil)
		return
	}
	if len([]rune(req.Channel)) > 64 || len([]rune(req.Policy)) > 64 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "channel or policy exceeds its length limit", nil)
		return
	}
	if req.Channel == "" {
		req.Channel = "direct"
	}
	if req.Policy == "" {
		req.Policy = "limited"
	}

	result, err := s.issueBootstrapGrantRecord(req, publicKey)
	if errors.Is(err, errConflict) {
		fail(c, http.StatusConflict, "ENTITLEMENT_CONFLICT", "this installation entitlement is bound to a different request", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "BOOTSTRAP_GRANT_FAILED", "could not issue bootstrap grant", nil)
		return
	}
	reply(c, http.StatusCreated, result.response())
}

func (s *Service) issueBootstrapGrantRecord(req issueGrantRequest, publicKey ed25519.PublicKey) (bootstrapGrantResult, error) {
	keyFingerprint := fingerprint(publicKey)
	entitlementHash := keyedHash(s.bootstrapSecret, req.EntitlementID)
	requestID := req.IdempotencyKey
	receiptSeed := entitlementHash + "\x00" + requestID + "\x00" + keyFingerprint
	grant := "efbg_" + keyedHash(s.bootstrapSecret, "grant\x00"+receiptSeed)
	nonce := "efn_" + keyedHash(s.bootstrapSecret, "nonce\x00"+receiptSeed)
	var expiresAt int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, entitlementHash).Error; err != nil {
			return err
		}
		var now int64
		if err := tx.Raw(`SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint`).Scan(&now).Error; err != nil {
			return err
		}
		expiresAt = now + int64(grantTTL/time.Millisecond)
		var existing struct {
			Fingerprint string `gorm:"column:key_fingerprint"`
			RequestID   string `gorm:"column:request_id"`
			Channel     string `gorm:"column:channel"`
			Policy      string `gorm:"column:policy"`
			Status      string `gorm:"column:status"`
			ExpiresAt   int64  `gorm:"column:expires_at"`
		}
		if err := tx.Raw(`SELECT key_fingerprint, request_id, channel, policy, status, expires_at
			FROM agent_bootstrap_grants WHERE entitlement_hash = ? FOR UPDATE`, entitlementHash).Scan(&existing).Error; err != nil {
			return err
		}
		if existing.Fingerprint != "" {
			if existing.Fingerprint != keyFingerprint || existing.RequestID != requestID ||
				existing.Channel != req.Channel || existing.Policy != req.Policy || existing.Status == "revoked" {
				return errConflict
			}
			if existing.Status == "issued" {
				if err := tx.Exec(`UPDATE agent_bootstrap_grants SET expires_at = ? WHERE entitlement_hash = ?`, expiresAt, entitlementHash).Error; err != nil {
					return err
				}
				return tx.Exec(`INSERT INTO agent_signature_nonces
					(nonce_hash, key_fingerprint, domain, expires_at, created_at)
					VALUES (?, ?, 'provision', ?, ?)
					ON CONFLICT (nonce_hash) DO UPDATE SET expires_at = EXCLUDED.expires_at, consumed_at = NULL`,
					hashString(nonce), keyFingerprint, expiresAt, now).Error
			}
			expiresAt = existing.ExpiresAt
			return nil
		}
		result := tx.Exec(`INSERT INTO agent_bootstrap_grants
			(jti_hash, key_fingerprint, audience, channel, policy, entitlement_hash,
			 request_id, status, expires_at, created_at)
			VALUES (?, ?, 'agent_provision', ?, ?, ?, ?, 'issued', ?, ?)`,
			hashString(grant), keyFingerprint, req.Channel, req.Policy,
			entitlementHash, requestID, expiresAt, now)
		if result.Error != nil {
			if isUniqueViolation(result.Error) {
				return errConflict
			}
			return result.Error
		}
		return tx.Exec(`INSERT INTO agent_signature_nonces
			(nonce_hash, key_fingerprint, domain, expires_at, created_at)
			VALUES (?, ?, 'provision', ?, ?)`, hashString(nonce), keyFingerprint, expiresAt, now).Error
	})
	if err != nil {
		return bootstrapGrantResult{}, err
	}
	return bootstrapGrantResult{
		BootstrapGrant: grant,
		Nonce:          nonce,
		ExpiresAt:      expiresAt,
		KeyFingerprint: keyFingerprint,
	}, nil
}

func subtleHeaderMismatch(got, want string) bool {
	if len(got) != len(want) {
		return true
	}
	var mismatch byte
	for i := range got {
		mismatch |= got[i] ^ want[i]
	}
	return mismatch != 0
}

type provisionRequest struct {
	BootstrapGrant  string            `json:"bootstrap_grant"`
	IdempotencyKey  string            `json:"idempotency_key"`
	Nonce           string            `json:"nonce"`
	PublicKey       string            `json:"public_key"`
	IssuedAt        int64             `json:"issued_at"`
	AgentName       string            `json:"agent_name"`
	Signature       string            `json:"signature"`
	Draft           json.RawMessage   `json:"onboarding_draft,omitempty"`
	FieldProvenance map[string]string `json:"field_provenance,omitempty"`
}

type provisionProofPayload struct {
	BootstrapGrant  string            `json:"bootstrap_grant"`
	IdempotencyKey  string            `json:"idempotency_key"`
	Nonce           string            `json:"nonce"`
	PublicKey       string            `json:"public_key"`
	IssuedAt        int64             `json:"issued_at"`
	AgentName       string            `json:"agent_name"`
	Draft           json.RawMessage   `json:"onboarding_draft,omitempty"`
	FieldProvenance map[string]string `json:"field_provenance,omitempty"`
}

func provisionTranscript(req provisionRequest) ([]byte, error) {
	payload := provisionProofPayload{
		BootstrapGrant:  req.BootstrapGrant,
		IdempotencyKey:  req.IdempotencyKey,
		Nonce:           req.Nonce,
		PublicKey:       req.PublicKey,
		IssuedAt:        req.IssuedAt,
		AgentName:       req.AgentName,
		Draft:           req.Draft,
		FieldProvenance: req.FieldProvenance,
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("EF-AUTH-V2\x00POST\n/api/v2/agent-identities/provision\n%s", hashString(string(canonical)))), nil
}

func provisionReceiptHash(req provisionRequest) (string, error) {
	payload := provisionProofPayload{
		BootstrapGrant: req.BootstrapGrant, IdempotencyKey: req.IdempotencyKey,
		Nonce: req.Nonce, PublicKey: req.PublicKey, AgentName: req.AgentName, Draft: req.Draft,
		FieldProvenance: req.FieldProvenance,
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return hashString(string(canonical)), nil
}

func normalizeDeviceName(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	if utf8.RuneCountInString(value) > 128 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", false
	}
	return value, true
}

func (s *Service) provision(ctx context.Context, c *app.RequestContext) {
	var req provisionRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), nil)
		return
	}
	publicKey, err := decodePublicKey(req.PublicKey)
	if err != nil || req.BootstrapGrant == "" || req.Nonce == "" || req.Signature == "" ||
		len(req.IdempotencyKey) < 16 || len(req.IdempotencyKey) > 128 {
		fail(c, http.StatusBadRequest, "INVALID_PROOF", "bootstrap grant, idempotency key, nonce, public key, and signature are required", nil)
		return
	}
	wallNow := time.Now().UnixMilli()
	if req.IssuedAt < wallNow-int64(proofClockSkew/time.Millisecond) || req.IssuedAt > wallNow+int64(proofClockSkew/time.Millisecond) {
		fail(c, http.StatusUnauthorized, "PROOF_EXPIRED", "proof timestamp is outside the accepted window", nil)
		return
	}
	signature, err := base64.RawURLEncoding.DecodeString(req.Signature)
	if err != nil {
		signature, err = base64.StdEncoding.DecodeString(req.Signature)
	}
	transcript, transcriptErr := provisionTranscript(req)
	if err != nil || transcriptErr != nil || !ed25519.Verify(publicKey, transcript, signature) {
		fail(c, http.StatusUnauthorized, "INVALID_PROOF", "Ed25519 proof verification failed", nil)
		return
	}
	if len([]rune(req.AgentName)) > 100 {
		fail(c, http.StatusBadRequest, "INVALID_AGENT_NAME", "agent_name exceeds 100 characters", nil)
		return
	}
	if strings.TrimSpace(req.AgentName) == "" {
		req.AgentName = "EigenFlux Agent"
	}
	deviceName, validDeviceName := normalizeDeviceName(string(c.GetHeader("X-Client-Device-Name")))
	if !validDeviceName {
		fail(c, http.StatusBadRequest, "INVALID_DEVICE_NAME", "device name is invalid", nil)
		return
	}
	if len(req.Draft) == 0 {
		req.Draft = json.RawMessage(`{}`)
	}
	if len(req.Draft) > 64<<10 {
		fail(c, http.StatusBadRequest, "INVALID_DRAFT", "onboarding_draft must be a JSON object no larger than 64KB", nil)
		return
	}
	normalizedDraft, draftObject, err := normalizeOnboardingDraftJSON(req.Draft)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_DRAFT", err.Error(), nil)
		return
	}
	if err := validateRequestedAgentProvenance(req.FieldProvenance); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_FIELD_PROVENANCE", err.Error(), nil)
		return
	}
	req.Draft = normalizedDraft
	initialProvenance, err := json.Marshal(deriveInitialProvenance(draftObject, provenanceAgent, req.FieldProvenance, wallNow))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_DRAFT", "could not derive onboarding field sources", nil)
		return
	}

	requestHash, receiptHashErr := provisionReceiptHash(req)
	if receiptHashErr != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "could not canonicalize provision receipt", nil)
		return
	}
	receiptSeed := fingerprint(publicKey) + "\x00" + req.IdempotencyKey + "\x00" + requestHash
	accessToken := "efv2a_" + keyedHash(s.otpPepper, "provision-access\x00"+receiptSeed)
	refreshToken := "efv2r_" + keyedHash(s.otpPepper, "provision-refresh\x00"+receiptSeed)
	familyID := "eff_" + keyedHash(s.otpPepper, "provision-family\x00"+receiptSeed)[:36]
	newAgentID, err := s.idgen.NextID()
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "ID_GENERATION_FAILED", "could not allocate Agent identity", nil)
		return
	}
	initialScopes := []string{"onboarding:write", "context:read", "feed:read", "notifications:ack", "commands:claim", "console:handoff:create"}
	keyFingerprint := fingerprint(publicKey)
	observedRuntime, _ := runtimeidentity.Parse(string(c.GetHeader("X-Client-Host")))
	var agentID, principalID, expiresAt int64
	created := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, keyFingerprint).Error; err != nil {
			return err
		}
		var now int64
		if err := tx.Raw(`SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint`).Scan(&now).Error; err != nil {
			return err
		}
		var receipt struct {
			AgentID             int64          `gorm:"column:agent_id"`
			PrincipalID         int64          `gorm:"column:principal_id"`
			ExpiresAt           int64          `gorm:"column:expires_at"`
			AbsoluteExpiresAt   int64          `gorm:"column:absolute_expires_at"`
			RevokedAt           *int64         `gorm:"column:revoked_at"`
			RotationRequestHash string         `gorm:"column:rotation_request_hash"`
			Scopes              pq.StringArray `gorm:"column:scopes;type:text[]"`
			PrincipalStatus     string         `gorm:"column:principal_status"`
		}
		if err := tx.Raw(`SELECT p.agent_id, cs.principal_id, cs.expires_at, cs.absolute_expires_at,
			cs.revoked_at, cs.rotation_request_hash, cs.scopes, p.status AS principal_status
			FROM agent_principals p JOIN agent_credential_sessions cs ON cs.principal_id = p.principal_id
			WHERE p.key_type = 'ed25519-v1' AND p.key_fingerprint = ?
			  AND cs.rotation_request_id = ? FOR UPDATE OF cs`, keyFingerprint,
			"provision:"+req.IdempotencyKey).Scan(&receipt).Error; err != nil {
			return err
		}
		if receipt.PrincipalID != 0 {
			if receipt.RotationRequestHash != requestHash {
				return errConflict
			}
			if receipt.RevokedAt != nil || receipt.AbsoluteExpiresAt <= now ||
				(receipt.PrincipalStatus != "limited" && receipt.PrincipalStatus != "active") {
				return errUnauthorized
			}
			agentID, principalID, expiresAt = receipt.AgentID, receipt.PrincipalID, receipt.ExpiresAt
			initialScopes = []string(receipt.Scopes)
			return nil
		}
		var grant struct {
			Fingerprint string `gorm:"column:key_fingerprint"`
			Status      string `gorm:"column:status"`
			ExpiresAt   int64  `gorm:"column:expires_at"`
		}
		if err := tx.Raw(`SELECT key_fingerprint, status, expires_at
			FROM agent_bootstrap_grants WHERE jti_hash = ? FOR UPDATE`, hashString(req.BootstrapGrant)).Scan(&grant).Error; err != nil {
			return err
		}
		if grant.Fingerprint == "" || grant.Fingerprint != keyFingerprint || grant.Status != "issued" || grant.ExpiresAt < now {
			return errUnauthorized
		}
		var nonceCount int64
		if err := tx.Raw(`SELECT COUNT(*) FROM agent_signature_nonces
			WHERE nonce_hash = ? AND key_fingerprint = ? AND domain = 'provision'
			  AND consumed_at IS NULL AND expires_at >= ?`, hashString(req.Nonce), keyFingerprint, now).Scan(&nonceCount).Error; err != nil {
			return err
		}
		if nonceCount != 1 {
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
			if existing.Status == "revoked" || existing.Status == "suspended" {
				return errUnauthorized
			}
			agentID, principalID = existing.AgentID, existing.PrincipalID
		} else {
			agentID = newAgentID
			aliasToken, tokenErr := randomToken("", 18)
			if tokenErr != nil {
				return tokenErr
			}
			alias := strings.ToLower(aliasToken) + "@identity.invalid"
			if err := insertProvisionedAgent(tx, agentID, alias, req.AgentName, now); err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO agent_profiles (agent_id, status, updated_at) VALUES (?, 0, ?)`, agentID, now).Error; err != nil {
				return err
			}
			if err := tx.Raw(`INSERT INTO agent_principals
				(agent_id, key_type, key_fingerprint, public_key, status, created_at, last_seen_at)
				VALUES (?, 'ed25519-v1', ?, ?, 'limited', ?, ?) RETURNING principal_id`,
				agentID, keyFingerprint, []byte(publicKey), now, now).Scan(&principalID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO agent_context_heads (agent_id, current_revision, updated_at)
				VALUES (?, 0, ?)`, agentID, now).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO agent_feed_v2_settings
				(agent_id, poll_interval_seconds, explicitly_set, updated_at)
				VALUES (?, 600, false, ?)`, agentID, now).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO agent_onboarding_v2
				(agent_id, state, current_step, revision, created_at, updated_at)
				VALUES (?, 'in_progress', 2, 1, ?, ?)`, agentID, now, now).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO agent_onboarding_drafts
				(agent_id, revision, draft_data, field_provenance, actor_type, request_id, created_at)
				VALUES (?, 1, ?::jsonb, ?::jsonb, 'agent_prefill', ?, ?)`,
				agentID, string(req.Draft), string(initialProvenance),
				"provision:"+hashString(req.BootstrapGrant), now).Error; err != nil {
				return err
			}
			created = true
		}

		grantUpdate := tx.Exec(`UPDATE agent_bootstrap_grants
			SET status = 'consumed', consumed_at = ?, consumed_by_agent_id = ?
			WHERE jti_hash = ? AND status = 'issued' AND consumed_at IS NULL AND expires_at >= ?`,
			now, agentID, hashString(req.BootstrapGrant), now)
		if grantUpdate.Error != nil || grantUpdate.RowsAffected != 1 {
			return errUnauthorized
		}
		nonceUpdate := tx.Exec(`UPDATE agent_signature_nonces SET consumed_at = ?
			WHERE nonce_hash = ? AND consumed_at IS NULL AND expires_at >= ?`, now, hashString(req.Nonce), now)
		if nonceUpdate.Error != nil || nonceUpdate.RowsAffected != 1 {
			return errUnauthorized
		}
		expiresAt = now + int64(accessTTL/time.Millisecond)
		return tx.Exec(`INSERT INTO agent_credential_sessions
			(principal_id, family_id, access_token_hash, refresh_token_hash, audience, scopes,
			 rotation_counter, issued_at, expires_at, absolute_expires_at, last_seen_at,
			 rotation_request_id, rotation_request_hash)
			VALUES (?, ?, ?, ?, 'agent_v2', ?, 0, ?, ?, ?, ?, ?, ?)`, principalID, familyID,
			hashString(accessToken), hashString(refreshToken), pq.Array(initialScopes),
			now, expiresAt, now+int64(refreshTTL/time.Millisecond), now,
			"provision:"+req.IdempotencyKey, requestHash).Error
	})
	if errors.Is(err, errUnauthorized) {
		fail(c, http.StatusUnauthorized, "BOOTSTRAP_PROOF_REJECTED", "bootstrap grant or nonce is invalid, consumed, or expired", nil)
		return
	}
	if errors.Is(err, errConflict) {
		fail(c, http.StatusConflict, "PROVISION_IDEMPOTENCY_CONFLICT", "idempotency key was used with a different provision request", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "PROVISION_FAILED", "could not provision Agent identity", nil)
		return
	}
	if created {
		agentcard.PublishRebuild(ctx, agentID, "agent_v2_provisioned")
	}
	publicIdentity, identityErr := agentidentity.Get(ctx, s.db, agentID)
	if identityErr != nil {
		fail(c, http.StatusInternalServerError, "PROVISION_IDENTITY_READ_FAILED", "could not load public Agent identity", nil)
		return
	}
	// Provision is the first authenticated request in Console V2 onboarding.
	// Persist its validated, self-reported product identity synchronously so the
	// claim page can name the actual runtime before the first settings heartbeat.
	if err := consoledal.UpdateHandoffClientIdentity(s.db, agentID, observedRuntime.Name, observedRuntime.Version, deviceName); err != nil {
		fail(c, http.StatusInternalServerError, "PROVISION_CLIENT_REPORT_FAILED", "could not record Agent client identity", nil)
		return
	}
	if created {
		activity.PublishAgentJoined(ctx, agentID)
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"agent_id":         fmt.Sprintf("%d", agentID),
		"short_id":         publicIdentity.ShortID,
		"eigenflux_id":     "eigenflux#" + publicIdentity.ShortID,
		"principal_id":     fmt.Sprintf("%d", principalID),
		"created":          created,
		"access_token":     accessToken,
		"refresh_token":    refreshToken,
		"expires_at":       expiresAt,
		"onboarding_state": "in_progress",
		"next_step":        2,
		"scopes":           initialScopes,
	})
}

func insertProvisionedAgent(tx *gorm.DB, agentID int64, alias, agentName string, now int64) error {
	for attempt := 0; attempt < 100; attempt++ {
		shortID, err := agentidentity.GenerateShortID()
		if err != nil {
			return err
		}
		result := tx.Exec(`INSERT INTO agents
			(agent_id, short_id, email, email_kind, agent_name, bio, created_at, updated_at)
			VALUES (?, ?, ?, 'internal_alias', ?, '', ?, ?)
			ON CONFLICT (short_id) WHERE short_id IS NOT NULL DO NOTHING`,
			agentID, shortID, alias, agentName, now, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		metrics.AgentShortIDGenerationCollisionTotal.Inc()
	}
	metrics.AgentShortIDGenerationFailureTotal.Inc()
	return errors.New("short-id collision retry budget exhausted")
}

type refreshChallengeRequest struct {
	RefreshToken      string `json:"refresh_token"`
	RotationRequestID string `json:"rotation_request_id"`
}

// createRefreshChallenge proves refresh-token possession before the caller
// performs the key proof. A consumed refresh token revokes the whole token
// family, which turns replay into an observable credential-compromise event.
func (s *Service) createRefreshChallenge(_ context.Context, c *app.RequestContext) {
	var req refreshChallengeRequest
	if err := decodeBody(c, &req); err != nil || !strings.HasPrefix(req.RefreshToken, "efv2r_") ||
		len(req.RotationRequestID) < 16 || len(req.RotationRequestID) > 128 {
		fail(c, http.StatusUnauthorized, "REFRESH_INVALID", "refresh credential is invalid or expired", nil)
		return
	}
	now := time.Now().UnixMilli()
	var row struct {
		FamilyID          string  `gorm:"column:family_id"`
		KeyFingerprint    string  `gorm:"column:key_fingerprint"`
		PrincipalStatus   string  `gorm:"column:principal_status"`
		AbsoluteExpiresAt int64   `gorm:"column:absolute_expires_at"`
		RevokedAt         *int64  `gorm:"column:revoked_at"`
		ReplacedBySession *int64  `gorm:"column:replaced_by_session_id"`
		SuccessorRequest  *string `gorm:"column:successor_request_id"`
	}
	if err := s.db.Raw(`SELECT cs.family_id, p.key_fingerprint, p.status AS principal_status,
		cs.absolute_expires_at, cs.revoked_at, cs.replaced_by_session_id,
		next.rotation_request_id AS successor_request_id
		FROM agent_credential_sessions cs
		JOIN agent_principals p ON p.principal_id = cs.principal_id
		LEFT JOIN agent_credential_sessions next ON next.session_id = cs.replaced_by_session_id
		WHERE cs.refresh_token_hash = ?`, hashString(req.RefreshToken)).Scan(&row).Error; err != nil || row.FamilyID == "" {
		fail(c, http.StatusUnauthorized, "REFRESH_INVALID", "refresh credential is invalid or expired", nil)
		return
	}
	if row.ReplacedBySession != nil && (row.SuccessorRequest == nil || *row.SuccessorRequest != req.RotationRequestID) {
		_ = s.db.Exec(`UPDATE agent_credential_sessions SET revoked_at = COALESCE(revoked_at, ?)
			WHERE family_id = ?`, now, row.FamilyID).Error
		fail(c, http.StatusUnauthorized, "REFRESH_REUSE_DETECTED", "refresh credential reuse revoked this session family", nil)
		return
	}
	if row.RevokedAt != nil && row.ReplacedBySession == nil {
		fail(c, http.StatusUnauthorized, "REFRESH_INVALID", "refresh credential is invalid or expired", nil)
		return
	}
	if row.AbsoluteExpiresAt <= now || (row.PrincipalStatus != "limited" && row.PrincipalStatus != "active") {
		fail(c, http.StatusUnauthorized, "REFRESH_INVALID", "refresh credential is invalid or expired", nil)
		return
	}
	nonce, err := randomToken("efn_", 32)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "could not create refresh challenge", nil)
		return
	}
	expiresAt := now + int64(2*time.Minute/time.Millisecond)
	if err := s.db.Exec(`INSERT INTO agent_signature_nonces
		(nonce_hash, key_fingerprint, domain, expires_at, created_at)
		VALUES (?, ?, 'refresh', ?, ?)`, hashString(nonce), row.KeyFingerprint, expiresAt, now).Error; err != nil {
		fail(c, http.StatusInternalServerError, "REFRESH_CHALLENGE_FAILED", "could not create refresh challenge", nil)
		return
	}
	reply(c, http.StatusCreated, map[string]interface{}{
		"nonce": nonce, "issued_at": now, "expires_at": expiresAt,
	})
}

type refreshAgentSessionRequest struct {
	RefreshToken      string `json:"refresh_token"`
	RotationRequestID string `json:"rotation_request_id"`
	Nonce             string `json:"nonce"`
	PublicKey         string `json:"public_key"`
	IssuedAt          int64  `json:"issued_at"`
	Signature         string `json:"signature"`
}

type refreshProofPayload struct {
	RefreshToken      string `json:"refresh_token"`
	RotationRequestID string `json:"rotation_request_id"`
	Nonce             string `json:"nonce"`
	PublicKey         string `json:"public_key"`
	IssuedAt          int64  `json:"issued_at"`
}

func refreshTranscript(req refreshAgentSessionRequest) ([]byte, error) {
	payload, err := json.Marshal(refreshProofPayload{
		RefreshToken: req.RefreshToken, RotationRequestID: req.RotationRequestID,
		Nonce:     req.Nonce,
		PublicKey: req.PublicKey,
		IssuedAt:  req.IssuedAt,
	})
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("EF-AUTH-V2-REFRESH\x00POST\n/api/v2/agent-sessions/refresh\n%s", hashString(string(payload)))), nil
}

func (s *Service) refreshAgentSession(_ context.Context, c *app.RequestContext) {
	var req refreshAgentSessionRequest
	if err := decodeBody(c, &req); err != nil || req.RefreshToken == "" || req.Nonce == "" || req.Signature == "" ||
		len(req.RotationRequestID) < 16 || len(req.RotationRequestID) > 128 {
		fail(c, http.StatusUnauthorized, "REFRESH_INVALID", "refresh proof is invalid or expired", nil)
		return
	}
	publicKey, err := decodePublicKey(req.PublicKey)
	now := time.Now().UnixMilli()
	if err != nil || req.IssuedAt < now-int64(proofClockSkew/time.Millisecond) || req.IssuedAt > now+int64(proofClockSkew/time.Millisecond) {
		fail(c, http.StatusUnauthorized, "REFRESH_INVALID", "refresh proof is invalid or expired", nil)
		return
	}
	signature, sigErr := base64.RawURLEncoding.DecodeString(req.Signature)
	if sigErr != nil {
		signature, sigErr = base64.StdEncoding.DecodeString(req.Signature)
	}
	transcript, transcriptErr := refreshTranscript(req)
	if sigErr != nil || transcriptErr != nil || !ed25519.Verify(publicKey, transcript, signature) {
		fail(c, http.StatusUnauthorized, "REFRESH_INVALID", "refresh proof is invalid or expired", nil)
		return
	}
	rotationHash := keyedHash(s.otpPepper, strings.Join([]string{
		"refresh-request", hashString(req.RefreshToken), fingerprint(publicKey), req.RotationRequestID,
	}, "\x00"))
	newAccessToken := "efv2a_" + keyedHash(s.otpPepper, "refresh-access\x00"+rotationHash)
	newRefreshToken := "efv2r_" + keyedHash(s.otpPepper, "refresh-token\x00"+rotationHash)

	var principalID, newSessionID, expiresAt int64
	var familyID string
	var scopes pq.StringArray
	reuseDetected := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var old struct {
			SessionID           int64          `gorm:"column:session_id"`
			PrincipalID         int64          `gorm:"column:principal_id"`
			FamilyID            string         `gorm:"column:family_id"`
			Scopes              pq.StringArray `gorm:"column:scopes;type:text[]"`
			RotationCounter     int64          `gorm:"column:rotation_counter"`
			AbsoluteExpiresAt   int64          `gorm:"column:absolute_expires_at"`
			RevokedAt           *int64         `gorm:"column:revoked_at"`
			ReplacedBySessionID *int64         `gorm:"column:replaced_by_session_id"`
			KeyFingerprint      string         `gorm:"column:key_fingerprint"`
			PrincipalStatus     string         `gorm:"column:principal_status"`
		}
		if err := tx.Raw(`SELECT cs.session_id, cs.principal_id, cs.family_id, cs.scopes,
			cs.rotation_counter, cs.absolute_expires_at, cs.revoked_at, cs.replaced_by_session_id,
			p.key_fingerprint, p.status AS principal_status
			FROM agent_credential_sessions cs
			JOIN agent_principals p ON p.principal_id = cs.principal_id
			WHERE cs.refresh_token_hash = ? FOR UPDATE`, hashString(req.RefreshToken)).Scan(&old).Error; err != nil {
			return err
		}
		if old.SessionID == 0 || old.KeyFingerprint != fingerprint(publicKey) ||
			(old.PrincipalStatus != "limited" && old.PrincipalStatus != "active") {
			return errUnauthorized
		}
		if err := tx.Raw(`SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint`).Scan(&now).Error; err != nil {
			return err
		}
		if old.AbsoluteExpiresAt <= now {
			return errUnauthorized
		}
		if old.ReplacedBySessionID != nil {
			var successor struct {
				SessionID           int64          `gorm:"column:session_id"`
				PrincipalID         int64          `gorm:"column:principal_id"`
				FamilyID            string         `gorm:"column:family_id"`
				Scopes              pq.StringArray `gorm:"column:scopes;type:text[]"`
				ExpiresAt           int64          `gorm:"column:expires_at"`
				AbsoluteExpiresAt   int64          `gorm:"column:absolute_expires_at"`
				RevokedAt           *int64         `gorm:"column:revoked_at"`
				RotationRequestID   *string        `gorm:"column:rotation_request_id"`
				RotationRequestHash *string        `gorm:"column:rotation_request_hash"`
			}
			if err := tx.Raw(`SELECT session_id, principal_id, family_id, scopes, expires_at,
				absolute_expires_at, revoked_at, rotation_request_id, rotation_request_hash
				FROM agent_credential_sessions WHERE session_id = ? FOR UPDATE`, *old.ReplacedBySessionID).
				Scan(&successor).Error; err != nil {
				return err
			}
			if successor.SessionID != 0 && successor.RevokedAt == nil && successor.AbsoluteExpiresAt > now &&
				successor.RotationRequestID != nil && *successor.RotationRequestID == req.RotationRequestID &&
				successor.RotationRequestHash != nil && *successor.RotationRequestHash == rotationHash {
				nonceUse := tx.Exec(`UPDATE agent_signature_nonces SET consumed_at = ?
					WHERE nonce_hash = ? AND key_fingerprint = ? AND domain = 'refresh'
					  AND consumed_at IS NULL AND expires_at >= ?`, now, hashString(req.Nonce), old.KeyFingerprint, now)
				if nonceUse.Error != nil || nonceUse.RowsAffected != 1 {
					return errUnauthorized
				}
				principalID, newSessionID, familyID = successor.PrincipalID, successor.SessionID, successor.FamilyID
				expiresAt, scopes = successor.ExpiresAt, successor.Scopes
				return nil
			}
			reuseDetected = true
			return tx.Exec(`UPDATE agent_credential_sessions SET revoked_at = COALESCE(revoked_at, ?)
				WHERE family_id = ?`, now, old.FamilyID).Error
		}
		if old.RevokedAt != nil {
			reuseDetected = true
			return tx.Exec(`UPDATE agent_credential_sessions SET revoked_at = COALESCE(revoked_at, ?)
				WHERE family_id = ?`, now, old.FamilyID).Error
		}
		nonceUse := tx.Exec(`UPDATE agent_signature_nonces SET consumed_at = ?
			WHERE nonce_hash = ? AND key_fingerprint = ? AND domain = 'refresh'
			  AND consumed_at IS NULL AND expires_at >= ?`, now, hashString(req.Nonce), old.KeyFingerprint, now)
		if nonceUse.Error != nil || nonceUse.RowsAffected != 1 {
			return errUnauthorized
		}
		expiresAt = now + int64(accessTTL/time.Millisecond)
		if expiresAt > old.AbsoluteExpiresAt {
			expiresAt = old.AbsoluteExpiresAt
		}
		principalID, familyID, scopes = old.PrincipalID, old.FamilyID, old.Scopes
		if err := tx.Raw(`INSERT INTO agent_credential_sessions
			(principal_id, family_id, access_token_hash, refresh_token_hash, audience, scopes,
			 rotation_counter, issued_at, expires_at, absolute_expires_at, last_seen_at,
			 rotation_request_id, rotation_request_hash)
			VALUES (?, ?, ?, ?, 'agent_v2', ?, ?, ?, ?, ?, ?, ?, ?) RETURNING session_id`,
			old.PrincipalID, old.FamilyID, hashString(newAccessToken), hashString(newRefreshToken),
			pq.Array([]string(old.Scopes)), old.RotationCounter+1, now, expiresAt, old.AbsoluteExpiresAt, now,
			req.RotationRequestID, rotationHash).
			Scan(&newSessionID).Error; err != nil {
			return err
		}
		update := tx.Exec(`UPDATE agent_credential_sessions
			SET revoked_at = ?, replaced_by_session_id = ?, last_seen_at = ?
			WHERE session_id = ? AND revoked_at IS NULL AND replaced_by_session_id IS NULL`,
			now, newSessionID, now, old.SessionID)
		if update.Error != nil || update.RowsAffected != 1 {
			return errConflict
		}
		return nil
	})
	if reuseDetected {
		fail(c, http.StatusUnauthorized, "REFRESH_REUSE_DETECTED", "refresh credential reuse revoked this session family", nil)
		return
	}
	if errors.Is(err, errUnauthorized) || errors.Is(err, errConflict) {
		fail(c, http.StatusUnauthorized, "REFRESH_INVALID", "refresh proof is invalid or expired", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "REFRESH_FAILED", "could not rotate Agent credentials", nil)
		return
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"principal_id":  fmt.Sprintf("%d", principalID),
		"session_id":    fmt.Sprintf("%d", newSessionID),
		"family_id":     familyID,
		"access_token":  newAccessToken,
		"refresh_token": newRefreshToken,
		"expires_at":    expiresAt,
		"scopes":        []string(scopes),
	})
}

type createHandoffRequest struct {
	BrowserNonce string `json:"browser_nonce"`
}

func (s *Service) createHandoff(_ context.Context, c *app.RequestContext) {
	agentID, ok := agentID(c)
	principalValue, principalOK := c.Get("principal_id")
	principalID, principalTypeOK := principalValue.(int64)
	if !ok || !principalOK || !principalTypeOK {
		fail(c, http.StatusUnauthorized, "AGENT_AUTH_REQUIRED", "Agent V2 authentication is required", nil)
		return
	}
	var req createHandoffRequest
	if raw, _ := c.Body(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			fail(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body", nil)
			return
		}
	}
	if len(req.BrowserNonce) < 32 || len(req.BrowserNonce) > 256 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "browser_nonce is required", nil)
		return
	}
	observedRuntime, _ := runtimeidentity.Parse(string(c.GetHeader("X-Client-Host")))
	deviceName, validDeviceName := normalizeDeviceName(string(c.GetHeader("X-Client-Device-Name")))
	if !validDeviceName {
		fail(c, http.StatusBadRequest, "INVALID_DEVICE_NAME", "device name is invalid", nil)
		return
	}
	if err := consoledal.UpdateHandoffClientIdentity(s.db, agentID, observedRuntime.Name, observedRuntime.Version, deviceName); err != nil {
		fail(c, http.StatusInternalServerError, "HANDOFF_CLIENT_REPORT_FAILED", "could not record Console client identity", nil)
		return
	}
	ticket, err := randomToken("efht_", 32)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "could not create handoff", nil)
		return
	}
	now := time.Now().UnixMilli()
	browserNonceHash := hashString(req.BrowserNonce)
	err = s.db.Exec(`INSERT INTO console_v2_handoffs
		(ticket_hash, agent_id, principal_id, console_scope, browser_nonce_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, hashString(ticket), agentID, principalID,
		pq.Array([]string{"console:onboarding", "console:read", "console:write"}), browserNonceHash,
		now+int64(handoffTTL/time.Millisecond), now).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "HANDOFF_FAILED", "could not create handoff", nil)
		return
	}
	reply(c, http.StatusCreated, map[string]interface{}{
		"handoff_url": s.publicURL + "/dashboard/handoff?ticket=" + url.QueryEscape(ticket) + "#nonce=" + url.QueryEscape(req.BrowserNonce),
		"expires_at":  now + int64(handoffTTL/time.Millisecond),
	})
}

type exchangeRequest struct {
	Ticket       string `json:"ticket"`
	BrowserNonce string `json:"browser_nonce,omitempty"`
}

func (s *Service) exchangeHandoff(_ context.Context, c *app.RequestContext) {
	var req exchangeRequest
	if err := decodeBody(c, &req); err != nil || req.Ticket == "" || len(req.BrowserNonce) < 32 || len(req.BrowserNonce) > 256 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "ticket and browser_nonce are required", nil)
		return
	}
	sessionID, sessionIDErr := randomToken("efcs_", 18)
	sessionSecret, sessionSecretErr := randomToken("", 32)
	csrfSecret, csrfErr := randomToken("efcsrf_", 24)
	if sessionIDErr != nil || sessionSecretErr != nil || csrfErr != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "could not establish Console V2 session", nil)
		return
	}
	var agentIDValue, principalID int64
	var scopes pq.StringArray
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var handoff struct {
			AgentID          int64          `gorm:"column:agent_id"`
			PrincipalID      int64          `gorm:"column:principal_id"`
			Scopes           pq.StringArray `gorm:"column:console_scope;type:text[]"`
			BrowserNonceHash *string        `gorm:"column:browser_nonce_hash"`
			ExpiresAt        int64          `gorm:"column:expires_at"`
		}
		if err := tx.Raw(`SELECT agent_id, principal_id, console_scope, browser_nonce_hash, expires_at
			FROM console_v2_handoffs
			WHERE ticket_hash = ? AND consumed_at IS NULL FOR UPDATE`, hashString(req.Ticket)).
			Scan(&handoff).Error; err != nil {
			return err
		}
		var now int64
		if err := tx.Raw(`SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint`).Scan(&now).Error; err != nil {
			return err
		}
		providedNonceHash := hashString(req.BrowserNonce)
		if handoff.AgentID == 0 || handoff.ExpiresAt < now || handoff.BrowserNonceHash == nil ||
			subtle.ConstantTimeCompare([]byte(providedNonceHash), []byte(*handoff.BrowserNonceHash)) != 1 {
			return errUnauthorized
		}
		consume := tx.Exec(`UPDATE console_v2_handoffs SET consumed_at = ?
			WHERE ticket_hash = ? AND consumed_at IS NULL AND expires_at >= ?`, now, hashString(req.Ticket), now)
		if consume.Error != nil || consume.RowsAffected != 1 {
			return errUnauthorized
		}
		agentIDValue, principalID, scopes = handoff.AgentID, handoff.PrincipalID, handoff.Scopes
		return tx.Exec(`INSERT INTO console_v2_sessions
				(session_id, session_secret_hash, agent_id, principal_id, csrf_secret_hash,
					 status, scopes, issued_at, idle_expires_at, absolute_expires_at, last_seen_at, auth_method)
				VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, 'handoff')`, sessionID, hashString(sessionSecret),
			agentIDValue, principalID, hashString(csrfSecret), pq.Array([]string(scopes)), now,
			now+int64(30*time.Minute/time.Millisecond), now+int64(12*time.Hour/time.Millisecond), now).Error
	})
	if errors.Is(err, errUnauthorized) {
		fail(c, http.StatusUnauthorized, "HANDOFF_INVALID", "handoff is invalid, consumed, or expired", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "HANDOFF_EXCHANGE_FAILED", "could not establish Console V2 session", nil)
		return
	}
	s.setConsoleCookie(c, sessionID+"."+sessionSecret, int((12*time.Hour)/time.Second))
	s.setCSRFCookie(c, csrfSecret, int((12*time.Hour)/time.Second))
	reply(c, http.StatusOK, map[string]interface{}{
		"agent_id":   fmt.Sprintf("%d", agentIDValue),
		"csrf_token": csrfSecret,
	})
}
