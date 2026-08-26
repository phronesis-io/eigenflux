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
		"This `0.4.0-dev.8` Skill requires CLI `0.0.34`",
		"The join task is incomplete until the final user-facing response contains that validated link",
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
		"我已经成功加入 EigenFlux 网络，接下来，需要你来为我做一些网络设置。",
		"[以人类伙伴身份继续 →](<console_url>)",
		"a non-empty `ticket` query parameter",
		"a non-empty `nonce` URL fragment",
		"replace only the URL scheme and host",
		"（链接 15 分钟内有效）",
		"Email binding is optional",
		"Store `geo` as one of `CN`, `HK`, `SG`, `JP`, `US`, `GB`, or `ZZ`",
		"Store `timezone` as one of `Asia/Shanghai`, `Asia/Singapore`, `Asia/Tokyo`",
		"Add provenance for every non-empty field path",
		"Use `agent_user_context` only",
		"Use Chinese for every generated free-text field when the user's conversation",
		"Store working languages only as `zh` and `en`",
		"Treat EigenFlux installation, provisioning, registration, onboarding, and test",
		"`agent_description`, `network_goal`, and `intent_actions` only from the user's",
		"If that evidence is absent, leave these fields empty for the human to complete",
		"MUST load the installed ef-broadcast and ef-communication Skills",
		"never substitute memory",
		"freshly read",
		"references/attention.md",
		"Commands → Feed → Attention → Publish",
		"legacy communication authentication failure skips only communication",
		"Missing evidence means the heartbeat failed",
	} {
		if !strings.Contains(onboarding, required) {
			t.Errorf("Console V2 onboarding contract is missing %q", required)
		}
	}
}

func TestPublicJoinEntryPointsPreferConsoleV2(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	requiredByFile := map[string][]string{
		"README.md": {
			"Agent join rule:",
			"eigenflux agent provision --help",
			"Do not request email or OTP",
		},
		"cli/cmd/root.go": {
			"eigenflux agent provision --draft-file -",
		},
		"cli/cmd/auth.go": {
			"Legacy email authentication commands",
			"New Agents must use eigenflux agent provision",
		},
		"static/templates/agti_join.tmpl.md": {
			"eigenflux agent provision",
			"Console V2",
		},
		"static/templates/skill.tmpl.md": {
			"Stable Agent provisioning",
			"email is optional inside Console V2",
		},
	}

	for rel, required := range requiredByFile {
		body, readErr := os.ReadFile(filepath.Join(repoRoot, rel))
		if readErr != nil {
			t.Fatalf("read %s: %v", rel, readErr)
		}
		text := string(body)
		for _, fragment := range required {
			if !strings.Contains(text, fragment) {
				t.Errorf("%s is missing Console V2 join contract %q", rel, fragment)
			}
		}
	}
}
