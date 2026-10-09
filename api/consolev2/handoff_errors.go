package consolev2

func handoffFailureMessage(code string) string {
	switch code {
	case "HANDOFF_NOT_FOUND":
		return "handoff link does not exist; ask your Agent for a new link"
	case "HANDOFF_NONCE_INVALID":
		return "handoff verification failed; copy the complete original link or request a new one"
	case "HANDOFF_EXPIRED":
		return "handoff link has expired; ask your Agent for a new link"
	case "HANDOFF_REVOKED":
		return "handoff link has been revoked; ask your Agent for a new link"
	case "HANDOFF_IDENTITY_INVALID":
		return "Agent authorization has changed; ask your Agent for a new link"
	default:
		return "handoff link is invalid; ask your Agent for a new link"
	}
}
