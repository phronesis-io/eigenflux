package cmd

import "cli.eigenflux.ai/internal/dispatch"

// Reception ownership does not grant automatic reply permission. The latter
// is checked against fresh owner control context before execution and sending.
func pmReceptionStatus(b dispatch.Binding) map[string]any {
	if !b.Handles("pm_push") {
		return map[string]any{
			"subscribed":                   false,
			"status":                       "not_subscribed",
			"reason":                       "binding_missing_pm_push",
			"heartbeat_owns_communication": true,
			"automatic_reply":              "requires_fresh_owner_permission",
		}
	}
	return map[string]any{
		"subscribed":                   true,
		"status":                       "subscribed_execution_unverified",
		"heartbeat_owns_communication": false,
		"automatic_reply":              "requires_fresh_owner_permission",
	}
}
