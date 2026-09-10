package commissionaccess

import "testing"

func TestAllowlistAllowsOnlyConfiguredAgentIDs(t *testing.T) {
	access, err := New(true, "42, 9223372036854775807, 42")
	if err != nil {
		t.Fatal(err)
	}
	for _, agentID := range []int64{42, 9223372036854775807} {
		if !access.Allows(agentID) {
			t.Fatalf("Agent %d was not allowed", agentID)
		}
	}
	if access.Allows(41) {
		t.Fatal("unconfigured Agent was allowed")
	}
}

func TestAllowlistRejectsMalformedValuesWithoutEchoingThem(t *testing.T) {
	for _, raw := range []string{"invalid-secret-value", "0", "-1", "1, nope"} {
		_, err := New(true, raw)
		if err == nil || err.Error() != "invalid COMMISSION_AGENT_ID_WHITELIST" {
			t.Fatalf("New(true, %q) error=%v", raw, err)
		}
	}
}

func TestEmptyEnabledAllowlistDeniesEveryAgent(t *testing.T) {
	for _, raw := range []string{"", "  ", ", ,"} {
		access, err := New(true, raw)
		if err != nil {
			t.Fatal(err)
		}
		if access.Allows(42) {
			t.Fatalf("New(true, %q) allowed Agent 42", raw)
		}
	}
}

func TestDisabledOrNilAllowlistAllowsPositiveAgents(t *testing.T) {
	access, err := New(false, "invalid value is ignored")
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []*Allowlist{nil, access} {
		if !policy.Allows(42) {
			t.Fatal("disabled policy rejected a positive Agent ID")
		}
		if policy.Allows(0) || policy.Allows(-1) {
			t.Fatal("policy allowed a non-positive Agent ID")
		}
	}
}
