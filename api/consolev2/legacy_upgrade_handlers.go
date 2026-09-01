package consolev2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"
)

// LegacyAgentUpgradeChallengeHandler binds a new stable installation key to
// the already-authenticated V1 Agent. The resulting grant cannot create or
// select a different Agent identity during V2 provision.
func (s *Service) LegacyAgentUpgradeChallengeHandler() app.HandlerFunc {
	return func(_ context.Context, c *app.RequestContext) {
		agentIDValue, ok := agentID(c)
		var req publicRegistrationChallengeRequest
		if !ok || decodeBody(c, &req) != nil {
			c.JSON(http.StatusBadRequest, map[string]interface{}{"code": 400, "msg": "invalid legacy Agent upgrade request", "data": nil})
			return
		}
		publicKey, err := decodePublicKey(req.PublicKey)
		if err != nil || len(req.IdempotencyKey) < 16 || len(req.IdempotencyKey) > 128 {
			c.JSON(http.StatusBadRequest, map[string]interface{}{"code": 400, "msg": "idempotency_key and a valid public_key are required", "data": nil})
			return
		}
		keyFingerprint := fingerprint(publicKey)
		var keyOwner int64
		if err := s.db.Raw(`SELECT agent_id FROM agent_principals
			WHERE key_type = 'ed25519-v1' AND key_fingerprint = ? AND revoked_at IS NULL`, keyFingerprint).Scan(&keyOwner).Error; err != nil {
			c.JSON(http.StatusInternalServerError, map[string]interface{}{"code": 500, "msg": "could not inspect Agent identity", "data": nil})
			return
		}
		if keyOwner != 0 && keyOwner != agentIDValue {
			c.JSON(http.StatusConflict, map[string]interface{}{"code": 409, "msg": "stable Agent Home is already bound to another Agent", "data": nil})
			return
		}
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, agentIDValue^int64(0x45_46_55_50)).Error; err != nil {
				return err
			}
			var now int64
			if err := tx.Raw(`SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint`).Scan(&now).Error; err != nil {
				return err
			}
			return ensureLegacyConsoleV2State(tx, agentIDValue, now)
		}); err != nil {
			c.JSON(http.StatusInternalServerError, map[string]interface{}{"code": 500, "msg": "could not prepare the existing Agent for upgrade", "data": nil})
			return
		}
		subjectAgentID := agentIDValue
		result, err := s.issueBootstrapGrantRecord(issueGrantRequest{
			EntitlementID:  fmt.Sprintf("legacy-agent-upgrade:%d:%s", agentIDValue, req.IdempotencyKey),
			IdempotencyKey: req.IdempotencyKey,
			Channel:        "legacy_in_place",
			Policy:         "limited",
			PublicKey:      strings.TrimSpace(req.PublicKey),
			SubjectAgentID: &subjectAgentID,
		}, publicKey)
		if errors.Is(err, errConflict) {
			c.JSON(http.StatusConflict, map[string]interface{}{"code": 409, "msg": "legacy Agent upgrade request conflicts with an existing grant", "data": nil})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]interface{}{"code": 500, "msg": "could not issue legacy Agent upgrade grant", "data": nil})
			return
		}
		data := result.response()
		data["agent_id"] = fmt.Sprintf("%d", agentIDValue)
		data["identity_preserved"] = true
		c.Header("Cache-Control", "private, no-store")
		c.JSON(http.StatusCreated, map[string]interface{}{"code": 0, "msg": "success", "data": data})
	}
}
