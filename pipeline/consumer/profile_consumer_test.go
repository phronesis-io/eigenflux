package consumer

import (
	"reflect"
	"testing"

	"eigenflux_server/pkg/config"
)

func TestBuildCachedProfile(t *testing.T) {
	profile := buildCachedProfile(42, []string{"ai-agents", "crypto"}, "Singapore")
	if profile.AgentID != 42 {
		t.Fatalf("AgentID=%d, want 42", profile.AgentID)
	}
	if len(profile.Keywords) != 2 || profile.Keywords[0] != "ai-agents" || profile.Keywords[1] != "crypto" {
		t.Fatalf("Keywords=%v, want [ai-agents crypto]", profile.Keywords)
	}
	if len(profile.Domains) != 2 || profile.Domains[0] != "ai-agents" || profile.Domains[1] != "crypto" {
		t.Fatalf("Domains=%v, want [ai-agents crypto]", profile.Domains)
	}
	if profile.Geo != "" {
		t.Fatalf("Geo=%q, want empty", profile.Geo)
	}
	if profile.GeoCountry != "Singapore" {
		t.Fatalf("GeoCountry=%q, want Singapore", profile.GeoCountry)
	}
}

func TestDeterministicTestProfileFeatures(t *testing.T) {
	keywords, country := deterministicTestProfileFeatures("Buyer research interest commission-run-123")
	if country != "" {
		t.Fatalf("country=%q, want empty", country)
	}
	want := []string{"buyer", "research", "interest", "commission-run-123"}
	if !reflect.DeepEqual(keywords, want) {
		t.Fatalf("keywords=%v, want %v", keywords, want)
	}
}

func TestDeterministicProfileModeRequiresCompleteIntegrationProfile(t *testing.T) {
	base := config.Config{
		AppEnv:                    "test",
		EnableCommissionIndex:     true,
		CommissionIntegrationFlag: "true",
		IntegrationControlAddr:    "127.0.0.1:19081",
		IntegrationControlToken:   "0123456789abcdef0123456789abcdef",
	}
	if !deterministicProfileMode(&base) {
		t.Fatal("complete integration profile did not enable deterministic profile features")
	}
	base.CommissionIntegrationFlag = ""
	if deterministicProfileMode(&base) {
		t.Fatal("APP_ENV=test enabled deterministic profile features without integration mode")
	}
	base.CommissionIntegrationFlag = "true"
	base.IntegrationControlToken = "short"
	if deterministicProfileMode(&base) {
		t.Fatal("invalid integration profile enabled deterministic profile features")
	}
}
