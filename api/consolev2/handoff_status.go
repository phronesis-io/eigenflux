package consolev2

import (
	"context"
	"crypto/subtle"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

// handoffStatus is read-only. Exchange remains the authoritative, locked check.
func (s *Service) handoffStatus(_ context.Context, c *app.RequestContext) {
	var req exchangeRequest
	if err := decodeBody(c, &req); err != nil || req.Ticket == "" || len(req.BrowserNonce) < 32 || len(req.BrowserNonce) > 256 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "ticket and browser_nonce are required", nil)
		return
	}
	var state struct {
		AgentID            int64
		PrincipalAgentID   int64
		PrincipalStatus    string
		PrincipalRevokedAt *int64
		IdentityState      string
		BrowserNonceHash   *string
		Expired            bool
		RevokedAt          *int64
		ConsumedAt         *int64
	}
	err := s.db.Raw(`SELECT h.agent_id, p.agent_id AS principal_agent_id,
 p.status AS principal_status, p.revoked_at AS principal_revoked_at, a.identity_state,
 h.browser_nonce_hash, h.expires_at < (extract(epoch FROM clock_timestamp())*1000)::bigint AS expired,
 h.revoked_at, h.consumed_at FROM console_v2_handoffs h
 LEFT JOIN agent_principals p ON p.principal_id = h.principal_id
 LEFT JOIN agents a ON a.agent_id = h.agent_id
 WHERE h.ticket_hash = ?`, hashString(req.Ticket)).Scan(&state).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "HANDOFF_EXCHANGE_FAILED", "could not check handoff availability", nil)
		return
	}
	code := ""
	nonceHash := hashString(req.BrowserNonce)
	switch {
	case state.AgentID == 0:
		code = "HANDOFF_NOT_FOUND"
	case state.BrowserNonceHash == nil || subtle.ConstantTimeCompare([]byte(nonceHash), []byte(*state.BrowserNonceHash)) != 1:
		code = "HANDOFF_NONCE_INVALID"
	case state.RevokedAt != nil:
		code = "HANDOFF_REVOKED"
	case state.Expired:
		code = "HANDOFF_EXPIRED"
	case !validActiveIdentityBinding(state.AgentID, state.PrincipalAgentID, state.IdentityState, state.PrincipalStatus, state.PrincipalRevokedAt):
		code = "HANDOFF_IDENTITY_INVALID"
	case state.ConsumedAt != nil:
		code = "HANDOFF_CONSUMED"
	}
	if code != "" {
		fail(c, http.StatusUnauthorized, code, handoffFailureMessage(code), nil)
		return
	}
	reply(c, http.StatusOK, map[string]interface{}{"available": true})
}
