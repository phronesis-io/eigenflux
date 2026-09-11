package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, repoRoot, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	// Git checkouts may use CRLF on Windows; prose contracts use canonical LF.
	return strings.ReplaceAll(string(body), "\r\n", "\n")
}

func TestProfileSkillOwnsOnlyPostOnboardingLifecycle(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	skill := readRepoFile(t, repoRoot, "skills/ef-profile/SKILL.md")
	for _, required := range []string{
		"eigenflux agent switch-account --help",
		"eigenflux agent provision --recover-account",
		"eigenflux agent switch-account",
		"Do not ask a clarifying question before generating the link",
		"Do not use the new-join four-line success template",
		"## Mandatory Intent Routing",
		"Keep these routes mutually exclusive",
		"A successful `eigenflux capabilities` or `eigenflux profile refresh-context` call confirms this route",
		"no active authenticated account",
		"Do not start provisioning, recovery, account switching, email verification, or OTP handling",
		"load `ef-onboarding`",
	} {
		if !strings.Contains(skill, required) {
			t.Errorf("ef-profile is missing lifecycle contract %q", required)
		}
	}

	frontmatter := strings.SplitN(skill, "---", 3)
	if len(frontmatter) != 3 {
		t.Fatal("ef-profile skill frontmatter is malformed")
	}
	for _, trigger := range []string{"regenerate the claim link", "switch account", "重新生成认领链接", "我要切换账号"} {
		if !strings.Contains(frontmatter[1], trigger) {
			t.Errorf("ef-profile frontmatter is missing account trigger %q", trigger)
		}
	}
	if !strings.Contains(frontmatter[1], `version: "0.9.2"`) {
		t.Error("ef-profile version was not advanced for the lifecycle split")
	}
	for _, forbidden := range []string{"## Mandatory Join Route", "## Install the CLI", "references/onboarding-v2.md"} {
		if strings.Contains(skill, forbidden) {
			t.Errorf("ef-profile still owns first-time setup %q", forbidden)
		}
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, "skills/ef-profile/references/onboarding-v2.md")); !os.IsNotExist(statErr) {
		t.Error("ef-profile still contains onboarding-v2.md")
	}

	for _, lifecycleRule := range []string{"no bound email", "temporary identity", "formal account", "selected again later"} {
		if !strings.Contains(skill, lifecycleRule) {
			t.Errorf("ef-profile is missing account lifecycle rule %q", lifecycleRule)
		}
	}
	for _, capabilityRule := range []string{
		"eigenflux capabilities --lang <zh-CN|en>", "stable `operation_id`", "eigenflux context goal set",
		"eigenflux context intent list", "eigenflux context security set", "show_add_friend", "Require fresh explicit human approval",
	} {
		if !strings.Contains(skill, capabilityRule) {
			t.Errorf("ef-profile capability routing is missing %q", capabilityRule)
		}
	}

	switchHelp := readRepoFile(t, repoRoot, "cli/cmd/agent_switch_account.go")
	for _, required := range []string{"Selecting the current account confirms it without changing credentials", "A different target account must be verified"} {
		if !strings.Contains(switchHelp, required) {
			t.Errorf("switch-account help is missing same-account contract %q", required)
		}
	}

	serverManagement := readRepoFile(t, repoRoot, "skills/ef-profile/references/server-management.md")
	for _, required := range []string{"eigenflux agent init --server staging", "eigenflux agent provision --server staging", "agent-v2-credentials.json"} {
		if !strings.Contains(serverManagement, required) {
			t.Errorf("server management reference is missing V2 routing %q", required)
		}
	}
	if strings.Contains(serverManagement, "eigenflux auth login") {
		t.Error("server management reference still routes users through legacy login")
	}
}

func TestOnboardingSkillContract(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	entry := readRepoFile(t, repoRoot, "skills/ef-onboarding/SKILL.md")
	for _, required := range []string{
		`version: "0.1.1"`,
		"references/consent.md",
		"references/prefill.md",
		"references/recurring-trigger.md",
		"references/console-handoff.md",
		"do not ask for installation consent again",
		"does not\npersist conversational authorization",
		"do not invoke\n`ef-broadcast` as a whole",
	} {
		if !strings.Contains(entry, required) {
			t.Errorf("ef-onboarding entry is missing %q", required)
		}
	}

	consent := readRepoFile(t, repoRoot, "skills/ef-onboarding/references/consent.md")
	for _, required := range []string{
		"EigenFlux 需要设置定时检查，并完成一次只读的初次网络检查；检查结果只会显示在你的 Console 中。你还可以允许我读取近期相关工作上下文，生成隐私过滤后的预填资料，并提交到 EigenFlux Console 供你审核。",
		"请回复「同意并预填」或「仅设置定时检查」。",
		"one read-only initial network check",
		"Agree and prefill",
		"Only set up scheduled checks",
		"do not infer Prefill permission",
	} {
		if !strings.Contains(consent, required) {
			t.Errorf("onboarding consent contract is missing %q", required)
		}
	}
	if strings.Contains(consent, "Install the EigenFlux CLI") || strings.Contains(consent, "安装 EigenFlux CLI") {
		t.Error("onboarding consent still asks for installation")
	}

	prefill := readRepoFile(t, repoRoot, "skills/ef-onboarding/references/prefill.md")
	for _, required := range []string{
		"manual path", "field_provenance", "agent_user_context", "agent_inferred",
		"`working_languages` protocol accepts only `zh` and `en`",
		"Never infer permission\nfor `network_action` or `trade_action`",
		"never include names, emails, credentials, internal URLs",
		"Do not enumerate, summarize, quote, or ask the user",
		"On the personalized path, `agent_name` must be non-empty",
		"The manual path keeps `agent_name` empty",
	} {
		if !strings.Contains(prefill, required) {
			t.Errorf("onboarding Prefill contract is missing %q", required)
		}
	}

	handoff := readRepoFile(t, repoRoot, "skills/ef-onboarding/references/console-handoff.md")
	for _, required := range []string{
		"eigenflux --homedir \"<agent-home>\" agent init --format json",
		"eigenflux --homedir \"<agent-home>\" agent provision --mode \"<installation-mode>\" --runtime-name \"<known-product>\" --draft-file -",
		"a non-empty `ticket` query parameter",
		"a non-empty `nonce` URL fragment",
		"[【点击此处，以人类伙伴身份继续 →】](<console_url>)",
		"[Continue as my human partner →](<console_url>)",
		"The Console always opens at Step 1",
		"Email verification is required before later\nonboarding steps",
		"An Agent ID change is not a reason to call provision again",
		"eigenflux --homedir \"<agent-home>\" feed poll --limit 20 --action refresh --format json",
		"`schema_version: feed.v2`",
		"`personalization.mode: baseline`",
		"ef-broadcast/references/attention.md",
		"eigenflux --homedir \"<agent-home>\" attention prefill --stdin --format json",
		"Do not load `ef-broadcast` as a whole",
		"does not\npublish Active Attention",
		"zero qualified items skips the upload and is a valid",
		"does\nnot authorize or perform Feed feedback",
		"Use this\nsame success template when personalization was declined",
	} {
		if !strings.Contains(handoff, required) {
			t.Errorf("Console handoff contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"feed feedback --items",
		"attention publish --stdin",
		"feed event record",
		"This manual-path response replaces the four-line success template",
	} {
		if strings.Contains(entry, forbidden) || strings.Contains(handoff, forbidden) {
			t.Errorf("ef-onboarding includes an operation outside the onboarding baseline %q", forbidden)
		}
	}
}

func TestConsoleV2SchedulerStoresOnlyHeartbeatLauncher(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	reference := readRepoFile(t, repoRoot, "skills/ef-onboarding/references/recurring-trigger.md")
	blocks := strings.Split(reference, "```text")
	if len(blocks) != 2 {
		t.Fatalf("scheduler reference must contain exactly one text launcher block: %s", reference)
	}
	launcherBlock := strings.SplitN(blocks[1], "```", 2)
	if len(launcherBlock) != 2 {
		t.Fatal("scheduler launcher block is not closed")
	}
	const want = "EIGENFLUX_MODE=\"<installation-mode>\" eigenflux --homedir \"<agent-home>\" heartbeat plan --format agent"
	if got := strings.TrimSpace(launcherBlock[0]); got != want {
		t.Fatalf("scheduler body must be the thin launcher only\nwant: %s\n got: %s", want, got)
	}
	for _, required := range []string{"Never create a duplicate", "OpenClaw or Claude Code", "WorkBuddy", "Codex", "read both back"} {
		if !strings.Contains(reference, required) {
			t.Errorf("scheduler contract is missing %q", required)
		}
	}
}

func TestPublicJoinEntryPointsUseOnboardingSkill(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	requiredByFile := map[string][]string{
		"cli/cmd/root.go":                    {"eigenflux agent provision --draft-file -"},
		"cli/cmd/auth.go":                    {"Legacy email authentication commands", "New Agents must use eigenflux agent provision"},
		"cli/scripts/install-local.sh":       {"Read ef-onboarding skill"},
		"skills/ef-broadcast/SKILL.md":       {"ef-onboarding/references/recurring-trigger.md"},
		"skills/ef-communication/SKILL.md":   {"ef-onboarding/references/recurring-trigger.md"},
		"static/install.ps1":                 {"Check ef-onboarding skill"},
		"static/install.sh":                  {"ef-broadcast|ef-communication|ef-onboarding|ef-profile", "Check ef-onboarding skill"},
		"static/templates/agti_join.tmpl.md": {"https://github.com/phronesis-io/eigenflux/blob/main/skills/install.md"},
		"static/templates/skill.tmpl.md":     {"https://github.com/phronesis-io/eigenflux/blob/main/skills/install.md", "ef-onboarding"},
	}

	for rel, required := range requiredByFile {
		text := readRepoFile(t, repoRoot, rel)
		for _, fragment := range required {
			if !strings.Contains(text, fragment) {
				t.Errorf("%s is missing Console V2 join contract %q", rel, fragment)
			}
		}
	}
}

func TestStandaloneInstallEntryOwnsHostInstallationRules(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	entry := readRepoFile(t, repoRoot, "skills/install.md")
	if strings.HasPrefix(entry, "---") {
		t.Fatal("skills/install.md must remain a standalone entry, not a distributable Skill")
	}
	for _, required := range []string{
		"separate conversational installation",
		"EIGENFLUX_SETUP_HOSTS=all",
		"EIGENFLUX_SKIP_AGENT_SETUP=1",
		"irm https://eigenflux.ai/install.ps1 | iex",
		"EIGENFLUX_INSTALL_DIR",
		"OpenClaw",
		"Codex",
		"Claude Code",
		"WorkBuddy",
		"eigenflux --homedir \"<agent-home>\" version",
		"eigenflux --homedir \"<agent-home>\" skills path",
		"load the installed `ef-onboarding` Skill",
		"consent question must be\n" +
			"the entire next user-visible response",
		"Keep successful CLI, Skill, plugin,\n" +
			"version, and Home verification details internal",
	} {
		if !strings.Contains(entry, required) {
			t.Errorf("standalone install entry is missing %q", required)
		}
	}
}
