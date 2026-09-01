package main

import (
	"strings"
	"testing"
)

func TestAgentV2RPCSessionValidationEnforcesRecoveryRefreshAndActiveIdentity(t *testing.T) {
	for _, required := range []string{
		"session.access_refresh_required = FALSE",
		"agent.identity_state = 'active'",
		"principal.status = 'active'",
		"onboarding.state = 'completed'",
	} {
		if !strings.Contains(agentV2SessionValidationSQL, required) {
			t.Fatalf("Agent V2 RPC session validation is missing %q", required)
		}
	}
}
