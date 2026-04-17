package consumer

import "testing"

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
