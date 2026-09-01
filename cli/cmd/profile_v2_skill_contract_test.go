package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func containsNormalizedText(body, fragment string) bool {
	normalize := func(value string) string { return strings.Join(strings.Fields(value), " ") }
	return strings.Contains(normalize(body), normalize(fragment))
}

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
		"Skill requires CLI `0.0.34`",
		"The join task is incomplete until the final user-facing response contains that validated link",
		"Resolve a review-ready onboarding draft",
		"Do not generate a Console handoff while the draft still expects the human to author",
		"references/upgrade-v2.md",
		"Never turn an upgrade into a new Agent registration",
	} {
		if !containsNormalizedText(skill, required) {
			t.Errorf("ef-profile is missing mandatory Console V2 routing text %q", required)
		}
	}

	onboardingBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-profile/references/onboarding-v2.md"))
	if err != nil {
		t.Fatalf("read Console V2 onboarding reference: %v", err)
	}
	onboarding := string(onboardingBody)
	for _, required := range []string{
		"EigenFlux 的接入准备已经完成。",
		"[【点击此处，审核并确认 →】](<console_url>)",
		"a non-empty `ticket` query parameter",
		"a non-empty `nonce` URL fragment",
		"replace only the URL scheme and host",
		"（链接 15 分钟内有效）",
		"Email binding is optional",
		"Store `geo` as one of `CN`, `HK`, `SG`, `JP`, `US`, `GB`, or `ZZ`",
		"Store `timezone` as one of `Asia/Shanghai`, `Asia/Singapore`, `Asia/Tokyo`",
		"Add provenance for every non-empty field path",
		"Use `agent_user_context` only",
		"Apply the `User Language` rule in the main Skill to every generated free-text",
		"`working_languages` protocol accepts only `zh` and `en`",
		"Treat EigenFlux installation, provisioning, registration, onboarding, and test",
		"The Console is the human's review surface",
		"there are no unresolved paths and the human does not need",
		"ask one concise, consolidated question",
		"Do not run `eigenflux agent provision`, generate the Console handoff, or return",
		"The following draft fields are public to every Agent on the network",
		"`network_goal`, and `intent_actions` are private",
		"may present product defaults for controls not previously",
		"The task body must contain only this launcher",
		"eigenflux --homedir \"<agent-home>\" heartbeat plan --format agent",
		"follow the returned plan in",
		"Never copy Feed, Attention, Communication, publishing, security",
		"OpenClaw or Claude",
		"never create a second scheduler beside the plugin",
		"Every heartbeat starts with `heartbeat plan`",
		"Set both the current Codex task title and its attached",
		"automation name to exactly `EigenFlux 网络收件箱`, then read both back",
		"succeeds only when both names match exactly",
	} {
		if !containsNormalizedText(onboarding, required) {
			t.Errorf("Console V2 onboarding contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"Do not interview the user before provisioning",
		"Unknown fields stay empty for the human to confirm in the Console",
		"leave these fields empty for the human to complete",
	} {
		if containsNormalizedText(onboarding, forbidden) {
			t.Errorf("Console V2 onboarding contract still permits a blank human-authored form: %q", forbidden)
		}
	}

	upgradeBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-profile/references/upgrade-v2.md"))
	if err != nil {
		t.Fatalf("read Console V2 upgrade reference: %v", err)
	}
	upgrade := string(upgradeBody)
	for _, required := range []string{
		"Preserve the existing identity, credentials, owner-confirmed profile values",
		"reconciled into a review-ready draft",
		"Do not generate or return a Console link until the readiness gate",
		"eigenflux --homedir \"<stable-home>\" agent provision --draft-file -",
		"must not create a replacement identity",
		"The Console is presented as a place to review and confirm Agent-prepared",
	} {
		if !containsNormalizedText(upgrade, required) {
			t.Errorf("Console V2 upgrade contract is missing %q", required)
		}
	}
}

func TestConsoleV2SchedulerStoresOnlyHeartbeatLauncher(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-profile/references/onboarding-v2.md"))
	if err != nil {
		t.Fatalf("read Console V2 onboarding reference: %v", err)
	}

	section := strings.SplitN(string(body), "## 3. Persist exactly one recurring trigger", 2)
	if len(section) != 2 {
		t.Fatal("Console V2 onboarding reference is missing scheduler section")
	}
	section = strings.SplitN(section[1], "## 4. Provision from the same Agent Home", 2)
	if len(section) != 2 {
		t.Fatal("Console V2 onboarding scheduler section has no boundary")
	}
	blocks := strings.Split(section[0], "```text")
	if len(blocks) != 2 {
		t.Fatalf("scheduler section must contain exactly one text launcher block: %s", section[0])
	}
	launcherBlock := strings.SplitN(blocks[1], "```", 2)
	if len(launcherBlock) != 2 {
		t.Fatal("scheduler launcher block is not closed")
	}
	const want = "eigenflux --homedir \"<agent-home>\" heartbeat plan --format agent"
	if got := strings.TrimSpace(launcherBlock[0]); got != want {
		t.Fatalf("scheduler body must be the thin launcher only\nwant: %s\n got: %s", want, got)
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
			"prepare a review-ready draft",
			"do not send the human to a blank Console form",
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
			"不要把空表单交给主人填写",
		},
		"static/templates/skill.tmpl.md": {
			"Stable Agent provisioning",
			"Email is optional inside Console V2",
			"prepare every editable value and resolve privacy choices",
		},
	}

	for rel, required := range requiredByFile {
		body, readErr := os.ReadFile(filepath.Join(repoRoot, rel))
		if readErr != nil {
			t.Fatalf("read %s: %v", rel, readErr)
		}
		text := string(body)
		for _, fragment := range required {
			if !containsNormalizedText(text, fragment) {
				t.Errorf("%s is missing Console V2 join contract %q", rel, fragment)
			}
		}
	}
}
