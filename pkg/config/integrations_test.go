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

func TestEnterpriseRateLimitsDefaultOffAndConfigurable(t *testing.T) {
	t.Setenv("ENTERPRISE_RATE_LIMIT_MODE", "")
	if got := Load().EnterpriseRateLimits.Mode; got != "off" {
		t.Fatalf("default mode = %q", got)
	}
	t.Setenv("ENTERPRISE_RATE_LIMIT_MODE", "observe")
	t.Setenv("ENTERPRISE_RATE_LIMIT_IP_PER_MINUTE", "21")
	t.Setenv("ENTERPRISE_RATE_LIMIT_TOTAL_PER_MINUTE", "210")
	t.Setenv("ENTERPRISE_RATE_LIMIT_APP_PER_MINUTE", "15")
	t.Setenv("ENTERPRISE_RATE_LIMIT_TRUSTED_PROXY_CIDRS", "127.0.0.1/32,::1/128")
	cfg := Load().EnterpriseRateLimits
	if cfg.Mode != "observe" || cfg.RequestsPerIP != 21 || cfg.RequestsTotal != 210 ||
		cfg.RequestsPerApp != 15 || len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatalf("unexpected enterprise rate limits: %+v", cfg)
	}
}
