package consolev2

import "testing"

func TestTodayCapabilitiesExposeStableAttentionFlag(t *testing.T) {
	service := &Service{enableControl: true, enableAttentionV1: true}
	capabilities := service.todayCapabilities()

	if capabilities["control_enabled"] != true {
		t.Fatalf("control_enabled = %#v, want true", capabilities["control_enabled"])
	}
	if capabilities["attention_enabled"] != true {
		t.Fatalf("attention_enabled = %#v, want true", capabilities["attention_enabled"])
	}
	if capabilities["agent_attention_v1_enabled"] != true {
		t.Fatalf("agent_attention_v1_enabled = %#v, want true", capabilities["agent_attention_v1_enabled"])
	}
}
