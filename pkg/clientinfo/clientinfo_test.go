package clientinfo

import (
	"context"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

func TestFromContext_AllValues(t *testing.T) {
	ctx := context.Background()
	ctx = metainfo.WithPersistentValue(ctx, KeySkillVer, "0.0.3")
	ctx = metainfo.WithPersistentValue(ctx, KeySkillVerNum, "3")

	ci := FromContext(ctx)
	if ci.SkillVer != "0.0.3" {
		t.Fatalf("SkillVer = %q, want %q", ci.SkillVer, "0.0.3")
	}
	if ci.SkillVerNum != 3 {
		t.Fatalf("SkillVerNum = %d, want %d", ci.SkillVerNum, 3)
	}
}

func TestFromContext_Empty(t *testing.T) {
	ci := FromContext(context.Background())
	if ci.SkillVer != "" {
		t.Fatalf("SkillVer = %q, want empty", ci.SkillVer)
	}
	if ci.SkillVerNum != 0 {
		t.Fatalf("SkillVerNum = %d, want 0", ci.SkillVerNum)
	}
}

func TestToVars(t *testing.T) {
	ci := ClientInfo{SkillVer: "0.0.3", SkillVerNum: 3}
	vars := ci.ToVars()
	if vars["skill_ver"] != "0.0.3" {
		t.Fatalf("skill_ver = %q, want %q", vars["skill_ver"], "0.0.3")
	}
	if vars["skill_ver_num"] != "3" {
		t.Fatalf("skill_ver_num = %q, want %q", vars["skill_ver_num"], "3")
	}
	if _, ok := vars["agent_id"]; ok {
		t.Fatal("agent_id should not be in ClientInfo.ToVars()")
	}
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	ctx = metainfo.WithPersistentValue(ctx, KeySkillVer, "1.2.3")
	ctx = metainfo.WithPersistentValue(ctx, KeySkillVerNum, "10203")

	vars := FromContext(ctx).ToVars()
	if vars["skill_ver"] != "1.2.3" {
		t.Fatalf("round-trip skill_ver = %q", vars["skill_ver"])
	}
	if vars["skill_ver_num"] != "10203" {
		t.Fatalf("round-trip skill_ver_num = %q", vars["skill_ver_num"])
	}
}
