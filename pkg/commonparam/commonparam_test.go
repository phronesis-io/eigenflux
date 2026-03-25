package commonparam

import (
	"context"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

func TestFromContext_AllValues(t *testing.T) {
	ctx := context.Background()
	ctx = metainfo.WithPersistentValue(ctx, KeySkillVer, "0.0.3")
	ctx = metainfo.WithPersistentValue(ctx, KeySkillVerNum, "3")
	ctx = metainfo.WithPersistentValue(ctx, KeyAgentID, "123456789")

	cp := FromContext(ctx)
	if cp.SkillVer != "0.0.3" {
		t.Fatalf("SkillVer = %q, want %q", cp.SkillVer, "0.0.3")
	}
	if cp.SkillVerNum != 3 {
		t.Fatalf("SkillVerNum = %d, want %d", cp.SkillVerNum, 3)
	}
	if cp.AgentID != 123456789 {
		t.Fatalf("AgentID = %d, want %d", cp.AgentID, 123456789)
	}
}

func TestFromContext_Empty(t *testing.T) {
	cp := FromContext(context.Background())
	if cp.SkillVer != "" {
		t.Fatalf("SkillVer = %q, want empty", cp.SkillVer)
	}
	if cp.SkillVerNum != 0 {
		t.Fatalf("SkillVerNum = %d, want 0", cp.SkillVerNum)
	}
	if cp.AgentID != 0 {
		t.Fatalf("AgentID = %d, want 0", cp.AgentID)
	}
}

func TestToVars(t *testing.T) {
	cp := CommonParam{
		SkillVer:    "0.0.3",
		SkillVerNum: 3,
		AgentID:     123,
	}
	vars := cp.ToVars()
	if vars["skill_ver"] != "0.0.3" {
		t.Fatalf("skill_ver = %q, want %q", vars["skill_ver"], "0.0.3")
	}
	if vars["skill_ver_num"] != "3" {
		t.Fatalf("skill_ver_num = %q, want %q", vars["skill_ver_num"], "3")
	}
	if vars["agent_id"] != "123" {
		t.Fatalf("agent_id = %q, want %q", vars["agent_id"], "123")
	}
}

func TestRoundTrip(t *testing.T) {
	// Write to metainfo, read back via FromContext, convert to vars
	ctx := context.Background()
	ctx = metainfo.WithPersistentValue(ctx, KeySkillVer, "1.2.3")
	ctx = metainfo.WithPersistentValue(ctx, KeySkillVerNum, "10203")
	ctx = metainfo.WithPersistentValue(ctx, KeyAgentID, "9876543210")

	cp := FromContext(ctx)
	vars := cp.ToVars()
	if vars["skill_ver"] != "1.2.3" {
		t.Fatalf("round-trip skill_ver = %q", vars["skill_ver"])
	}
	if vars["skill_ver_num"] != "10203" {
		t.Fatalf("round-trip skill_ver_num = %q", vars["skill_ver_num"])
	}
	if vars["agent_id"] != "9876543210" {
		t.Fatalf("round-trip agent_id = %q", vars["agent_id"])
	}
}
