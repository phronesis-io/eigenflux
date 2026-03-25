package authinfo

import (
	"context"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

func TestFromContext_WithValue(t *testing.T) {
	ctx := context.Background()
	ctx = metainfo.WithPersistentValue(ctx, KeyAgentID, "123456789")

	ai := FromContext(ctx)
	if ai.AgentID != 123456789 {
		t.Fatalf("AgentID = %d, want %d", ai.AgentID, 123456789)
	}
}

func TestFromContext_Empty(t *testing.T) {
	ai := FromContext(context.Background())
	if ai.AgentID != 0 {
		t.Fatalf("AgentID = %d, want 0", ai.AgentID)
	}
}

func TestToVars(t *testing.T) {
	ai := AuthInfo{AgentID: 123}
	vars := ai.ToVars()
	if vars["agent_id"] != "123" {
		t.Fatalf("agent_id = %q, want %q", vars["agent_id"], "123")
	}
	if _, ok := vars["skill_ver"]; ok {
		t.Fatal("skill_ver should not be in AuthInfo.ToVars()")
	}
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	ctx = metainfo.WithPersistentValue(ctx, KeyAgentID, "9876543210")

	vars := FromContext(ctx).ToVars()
	if vars["agent_id"] != "9876543210" {
		t.Fatalf("round-trip agent_id = %q", vars["agent_id"])
	}
}
