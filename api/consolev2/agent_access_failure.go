package consolev2

// Classify only server-owned credential state. Never include credential material
// in a reason or change the authorization predicates when adding observability.
func (p agentPrincipal) accessRejectionReason(now int64) string {
	switch {
	case p.SessionID == 0 || p.AgentID == 0:
		return "credential_not_found"
	case p.Audience != "agent_v2":
		return "audience_invalid"
	case p.RevokedAt != nil:
		return "credential_revoked"
	case p.PrincipalRevokedAt != nil:
		return "principal_revoked"
	case p.Status != "limited" && p.Status != "active":
		return "principal_inactive"
	case p.IdentityState != "active":
		return "identity_inactive"
	case p.AccessRefreshRequired:
		return "access_refresh_required"
	case p.ExpiresAt <= now:
		return "access_expired"
	default:
		return ""
	}
}
