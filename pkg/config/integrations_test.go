package config

import "testing"

func TestEnterpriseRoutesDefaultOff(t *testing.T) {
	for _, tc := range []struct {
		value   string
		enabled bool
	}{{"", false}, {"false", false}, {"true", true}, {"invalid", false}} {
		t.Setenv("ENABLE_ENTERPRISE_INTEGRATIONS", tc.value)
		if got := Load().EnableSDKIntegrations; got != tc.enabled {
			t.Fatalf("value %q: enabled=%t", tc.value, got)
		}
	}
}
