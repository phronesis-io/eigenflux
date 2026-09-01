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
		"Skill requires CLI `0.0.35`",
		"The join task is incomplete until the final user-facing response contains that validated link",
		"Do not run the public installer or `eigenflux skills sync`",
		"eigenflux agent provision --recover-account",
		"Do not run `eigenflux dashboard` or ordinary `eigenflux agent provision`",
		"Do not ask a clarifying question before generating the link",
		"Do not use the new-join four-line success template",
	} {
		if !strings.Contains(skill, required) {
			t.Errorf("ef-profile is missing mandatory Console V2 routing text %q", required)
		}
	}
	frontmatterParts := strings.SplitN(skill, "---", 3)
	if len(frontmatterParts) != 3 {
		t.Fatal("ef-profile skill frontmatter is malformed")
	}
	for _, trigger := range []string{"regenerate the claim link", "switch account", "重新生成认领链接", "我要切换账号"} {
		if !strings.Contains(frontmatterParts[1], trigger) {
			t.Errorf("ef-profile frontmatter is missing account recovery trigger %q", trigger)
		}
	}
	if !strings.Contains(frontmatterParts[1], `version: "0.5.0"`) {
		t.Error("ef-profile version was not advanced for the Attention Prefill contract")
	}
	for _, lifecycleRule := range []string{"has no bound email", "temporary identity", "formal account", "switch back later"} {
		if !strings.Contains(skill, lifecycleRule) {
			t.Errorf("ef-profile skill is missing account lifecycle rule %q", lifecycleRule)
		}
	}

	onboardingBody, err := os.ReadFile(filepath.Join(repoRoot, "skills/ef-profile/references/onboarding-v2.md"))
	if err != nil {
		t.Fatalf("read Console V2 onboarding reference: %v", err)
	}
	onboarding := string(onboardingBody)
	for _, required := range []string{
		"我已经成功加入 EigenFlux 网络。",
		"[【点击此处，以人类伙伴身份继续 →】](<console_url>)",
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
		"`agent_description`, `network_goal`, and `intent_actions` only from the user's",
		"If that evidence is absent, leave these fields empty for the human to complete",
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
		"Recovery transfers the current Home's Ed25519 principal",
		"An Agent ID change is not a reason to call provision again",
		"Requests to \"regenerate the claim link\" or \"switch account\"",
		"重新生成认领链接",
		"我要切换账号",
	} {
		if !strings.Contains(onboarding, required) {
			t.Errorf("Console V2 onboarding contract is missing %q", required)
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
