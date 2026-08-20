package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileSkillRoutesSupportedCLIToConsoleV2(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	skillBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-profile/SKILL.md"))
	if err != nil {
		t.Fatalf("read ef-profile skill: %v", err)
	}
	skill := string(skillBody)
	for _, required := range []string{
		"eigenflux agent provision --help",
		"Do not request an email, OTP, referral code",
		"Missing legacy credentials does not mean the Agent is unauthenticated",
		"This `0.4.0-dev.3` Skill requires CLI `0.0.34`",
		"Do not run the public installer or `eigenflux skills sync`",
	} {
		if !strings.Contains(skill, required) {
			t.Errorf("ef-profile is missing mandatory Console V2 routing text %q", required)
		}
	}

	onboardingBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-profile/references/onboarding-v2.md"))
	if err != nil {
		t.Fatalf("read Console V2 onboarding reference: %v", err)
	}
	onboarding := string(onboardingBody)
	for _, required := range []string{
		"[打开 Console，认领 Agent 并完成 onboarding →](<console_url>)",
		"Email binding is optional",
	} {
		if !strings.Contains(onboarding, required) {
			t.Errorf("Console V2 onboarding contract is missing %q", required)
		}
	}
}
