package authinfo

import (
	"context"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

func TestFromContext_WithValue(t *testing.T) {
	ctx := context.Background()
	ctx = metainfo.WithPersistentValue(ctx, KeyAgentID, "123456789")
	ctx = metainfo.WithPersistentValue(ctx, KeyEmail, "test@example.com")

	ai := FromContext(ctx)
	if ai.AgentID != 123456789 {
		t.Fatalf("AgentID = %d, want %d", ai.AgentID, 123456789)
	}
	if ai.Email != "test@example.com" {
		t.Fatalf("Email = %q, want %q", ai.Email, "test@example.com")
	}
}

func TestFromContext_Empty(t *testing.T) {
	ai := FromContext(context.Background())
	if ai.AgentID != 0 {
		t.Fatalf("AgentID = %d, want 0", ai.AgentID)
	}
	if ai.Email != "" {
		t.Fatalf("Email = %q, want empty", ai.Email)
	}
}

func TestToVars(t *testing.T) {
	ai := AuthInfo{AgentID: 123, Email: "user@example.com"}
	vars := ai.ToVars()
	if vars["agent_id"] != "123" {
		t.Fatalf("agent_id = %q, want %q", vars["agent_id"], "123")
	}
	if vars["email"] != "user@example.com" {
		t.Fatalf("email = %q, want %q", vars["email"], "user@example.com")
	}
	if _, ok := vars["skill_ver"]; ok {
		t.Fatal("skill_ver should not be in AuthInfo.ToVars()")
	}
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	ctx = metainfo.WithPersistentValue(ctx, KeyAgentID, "9876543210")
	ctx = metainfo.WithPersistentValue(ctx, KeyEmail, "roundtrip@example.com")

	ai := FromContext(ctx)
	vars := ai.ToVars()
	if vars["agent_id"] != "9876543210" {
		t.Fatalf("round-trip agent_id = %q", vars["agent_id"])
	}
	if vars["email"] != "roundtrip@example.com" {
		t.Fatalf("round-trip email = %q", vars["email"])
	}
}
