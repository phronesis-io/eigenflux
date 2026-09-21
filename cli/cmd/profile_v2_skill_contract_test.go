package cmd

import (
	"os"
	"path/filepath"
	"regexp"
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
	if !strings.Contains(frontmatter[1], `version: "0.9.7"`) {
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
		`version: "0.2.12"`,
		"references/consent.md",
		"Treat incomplete V2 setup as existing-account maintenance",
		"only when the user explicitly requests it",
		"https://cdn.eigenflux.ai/skills/latest/install.md#verify-and-continue",
		"Require both CLI compatibility",
		"and current-host installation verification",
		"Accept a supported\nbare-CLI setup",
		"references/prefill.md",
		"references/recurring-trigger.md",
		"references/console-handoff.md",
		"do not ask for installation consent again",
		"Reuse explicit choices in the original task",
		"references/execution-permission.md",
		"references/activation.md",
		"do not invoke\n`ef-broadcast` as a whole",
	} {
		if !strings.Contains(entry, required) {
			t.Errorf("ef-onboarding entry is missing %q", required)
		}
	}

	consent := readRepoFile(t, repoRoot, "skills/ef-onboarding/references/consent.md")
	for _, required := range []string{
		"one read-only initial network check",
		"do not create the trigger yet",
		"both required choices and host activation succeed",
		"Scheduling,\nexecution permission, and optional Prefill are separate decisions",
		"before personal-context retrieval, identity initialization",
		"never count as Prefill approval",
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
		"eigenflux --homedir \"<agent-home>\" agent provision --mode \"<installation-mode>\" --runtime-name \"<known-product>\" --draft-json '<draft-json>'",
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
		"eigenflux --homedir \"<agent-home>\" attention prefill --json '<batch>' --format json",
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

func TestConsoleV2SchedulerPromptMatchesCLI(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	reference := readRepoFile(t, repoRoot, "skills/ef-onboarding/references/recurring-trigger.md")
	blocks := strings.Split(reference, "```text")
	if len(blocks) != 3 {
		t.Fatal("expected the fixed prompt and a separate launcher block")
	}
	prompt := strings.TrimSpace(strings.SplitN(blocks[1], "```", 2)[0])
	launcher := strings.TrimSpace(strings.SplitN(blocks[2], "```", 2)[0])
	if strings.ReplaceAll(prompt, "<launcher>", launcher) != heartbeatSchedulerPrompt(launcher) {
		t.Fatal("onboarding and CLI generate different scheduler prompts")
	}
	if !strings.HasPrefix(launcher, "eigenflux --homedir ") || !strings.Contains(launcher, "--runtime-mode skill heartbeat plan") {
		t.Fatalf("launcher is not a direct, mode-explicit CLI call: %s", launcher)
	}
	for _, required := range []string{"reuse the owned EigenFlux trigger", "OpenClaw or Claude Code", "WorkBuddy", "Codex", "read back the trigger", "Compare the full stored prompt", "Do not paraphrase, shorten, prepend, or append", "update the same"} {
		if !strings.Contains(reference, required) {
			t.Errorf("scheduler contract missing %q", required)
		}
	}
}

func TestPublicJoinEntryPointsUseOnboardingSkill(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	requiredByFile := map[string][]string{
		"cli/cmd/root.go":                    {"eigenflux agent provision --draft-json '<draft-json>'"},
		"cli/cmd/auth.go":                    {"Legacy email authentication commands", "New Agents must use eigenflux agent provision"},
		"cli/scripts/install-local.sh":       {"Read ef-onboarding skill"},
		"skills/ef-broadcast/SKILL.md":       {"ef-onboarding/references/recurring-trigger.md"},
		"skills/ef-communication/SKILL.md":   {"ef-onboarding/references/recurring-trigger.md"},
		"static/install.ps1":                 {"Check ef-onboarding skill"},
		"static/install.sh":                  {"ef-broadcast|ef-communication|ef-onboarding|ef-profile", "Check ef-onboarding skill"},
		"static/templates/agti_join.tmpl.md": {"https://github.com/phronesis-io/eigenflux/blob/main/skills/install.md"},
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
		"eigenflux --homedir \"<agent-home>\" skills path --host \"<skill-host>\"",
		"`claude-code` for the corresponding macOS/Linux integration",
		"explicit `EIGENFLUX_SKILLS_DIR` or Home-scoped registered target",
		"A successfully installed Codex plugin awaiting activation may continue",
		"Load the installed `ef-onboarding` Skill",
		"scheduled-check question must\nbe the entire next user-visible response",
		"Keep successful CLI, Skill, plugin, version, and Home verification details",
		"check the current account in the same Home and server",
		"first incomplete stage using confirmed choices",
	} {
		if !strings.Contains(entry, required) {
			t.Errorf("standalone install entry is missing %q", required)
		}
	}
}

func TestOnboardingAuthorizationAndActivationBoundaries(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	entry := readRepoFile(t, repoRoot, "skills/ef-onboarding/SKILL.md")
	stages := []string{"**Scheduled checks.**", "**Execution permission.**", "**Activate.**", "**Optional Prefill.**", "**Initialize and draft.**", "**Schedule.**", "**Provision and connect.**"}
	previous := -1
	for _, stage := range stages {
		index := strings.Index(entry, stage)
		if index <= previous {
			t.Fatalf("setup stage missing or out of order: %s", stage)
		}
		previous = index
	}
	requiredByFile := map[string][]string{
		"skills/ef-onboarding/references/execution-permission.md": {
			"Refusal pauses first-time connection", "Write\nonly after approval",
			"trailing flags can override the target", "across Codex tasks",
			"never replace a conflicting", "do not write a duplicate",
			"an offline match proves the running host loaded the rule",
		},
		"skills/ef-onboarding/references/activation.md": {
			"one full quit and reopen", "original task's confirmed tool history",
			"grants no missing Rules or Prefill permission", "history is unavailable",
			"Home, server,\nor permission scope changed", "A user-disabled trigger is not missing",
			"An offline rule check alone is insufficient",
		},
		"skills/ef-onboarding/references/recurring-trigger.md": {
			"separate required scheduling", "execution-permission choices and host activation",
			"explicit server selection", "read back the trigger",
		},
	}
	for file, fragments := range requiredByFile {
		body := readRepoFile(t, repoRoot, file)
		for _, fragment := range fragments {
			if !strings.Contains(body, fragment) {
				t.Errorf("%s is missing boundary %q", file, fragment)
			}
		}
	}
	consent := readRepoFile(t, repoRoot, "skills/ef-onboarding/references/consent.md")
	// User-facing scheduling copy must not acquire either unrelated permission.
	scheduling := strings.SplitN(consent, "## Optional profile Prefill", 2)[0]
	for _, line := range strings.Split(scheduling, "\n") {
		if strings.HasPrefix(line, ">") && (strings.Contains(line, "Rules") || strings.Contains(line, "预填") || strings.Contains(line, "许可")) {
			t.Fatalf("scheduling question contains another permission: %s", line)
		}
	}
}

func TestOnboardingFixedTemplateCoverage(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	entry := readRepoFile(t, root, "skills/ef-onboarding/SKILL.md")
	for _, requirement := range []string{
		"choice labels verbatim", "Do not paraphrase, shorten, reorder, omit",
		"only variables or variants", "Failure handling, required host approvals",
		"template body and exact choice labels once, directly in chat",
		"explicit reply before dependent actions", "natural-language",
		"No answer grants no permission",
		"Keep only the current choice pending and preserve established choices on resume",
		"Host-native execution approvals",
	} {
		if !strings.Contains(entry, requirement) {
			t.Errorf("missing output constraint: %s", requirement)
		}
	}
	variable := regexp.MustCompile(`<[^>]+>`)
	for _, tc := range []struct {
		file    string
		bodies  int
		allowed map[string]bool
	}{
		{"consent.md", 8, map[string]bool{"<context-sources>": true}},
		{"execution-permission.md", 2, map[string]bool{"<rules-file>": true, "<rule-block>": true}},
		{"activation.md", 2, nil},
	} {
		body := readRepoFile(t, root, "skills/ef-onboarding/references/"+tc.file)
		blocks := []string{}
		current := []string{}
		for _, line := range strings.Split(body+"\n", "\n") {
			if strings.HasPrefix(line, ">") {
				current = append(current, strings.TrimPrefix(line, ">"))
			} else if len(current) > 0 {
				blocks = append(blocks, strings.Join(current, "\n"))
				current = nil
			}
		}
		if len(blocks) != tc.bodies {
			t.Errorf("%s: got %d localized bodies, want %d", tc.file, len(blocks), tc.bodies)
		}
		for _, block := range blocks {
			for _, placeholder := range variable.FindAllString(block, -1) {
				if !tc.allowed[placeholder] {
					t.Errorf("%s contains an undeclared template variable %s", tc.file, placeholder)
				}
			}
			if tc.file == "execution-permission.md" {
				for placeholder := range tc.allowed {
					if strings.Count(block, placeholder) != 1 {
						t.Errorf("permission body must show %s once", placeholder)
					}
				}
			}
		}
	}
}

func TestExecutionPermissionExistingRuleVariants(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	body := readRepoFile(t, root, "skills/ef-onboarding/references/execution-permission.md")
	localized := map[string]string{}
	for _, tc := range []struct{ language, start, end string }{
		{"Chinese", "Simplified Chinese:", "English:"},
		{"English", "English:", "Keep the read/write scope"},
	} {
		section := strings.SplitN(strings.SplitN(body, tc.start, 2)[1], tc.end, 2)[0]
		localized[tc.language] = strings.ReplaceAll(strings.ReplaceAll(section,
			"<rules-file>", "/tmp/nondefault codex/rules/eigenflux.rules"),
			"<rule-block>", `prefix_rule(pattern=["eigenflux", "--homedir", "/tmp/agent home/.eigenflux", "--server", "staging"], decision="allow")`)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(body, "\n") {
		cols := strings.Split(line, "|")
		if len(cols) != 5 {
			continue
		}
		lang := strings.TrimSpace(cols[1])
		rendered, ok := localized[lang]
		if !ok {
			continue
		}
		original, replacement := strings.TrimSpace(cols[2]), strings.TrimSpace(cols[3])
		if strings.Count(rendered, original) != 1 {
			t.Fatalf("%s variant does not match exactly once: %s", lang, original)
		}
		localized[lang] = strings.Replace(rendered, original, replacement, 1)
		counts[lang]++
	}
	for lang, rendered := range localized {
		if counts[lang] != 4 {
			t.Errorf("%s: incomplete existing-rule substitutions", lang)
		}
		if strings.Count(rendered, "> - **") != 2 {
			t.Errorf("%s: choices must remain distinct", lang)
		}
		if !strings.Contains(rendered, "/tmp/nondefault codex/rules/eigenflux.rules") || !strings.Contains(rendered, `"--server", "staging"`) {
			t.Errorf("%s: rule identity changed during substitution", lang)
		}
	}
}

func TestHeartbeatQuietResultsPreserveHostProtocol(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	reference := readRepoFile(t, repoRoot, "skills/ef-broadcast/references/heartbeat-execution.md")
	for _, required := range []string{
		"an empty message or silence token in place of required output",
		"host requires XML",
		"for other hosts use their actual schema",
		"Do not invent tags, identifiers, or a fallback schema",
		"not successful no-update handling",
	} {
		if !strings.Contains(reference, required) {
			t.Errorf("host-result contract missing %q", required)
		}
	}
	launcher := "eigenflux --homedir '/tmp/nondefault home' --server 'staging-network' --runtime-mode skill heartbeat plan --format agent"
	prompt := heartbeatSchedulerPrompt(launcher)
	if !strings.Contains(prompt, "Execute directly: "+launcher+".") {
		t.Fatal("scheduler prompt changed explicit identity or server")
	}
	if !strings.Contains(prompt, "never an empty message or silence token") {
		t.Fatal("scheduler prompt does not preserve required quiet host output")
	}
}

func TestExistingSchedulerCompatibilityContract(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	reference := readRepoFile(t, root, "skills/ef-onboarding/references/recurring-trigger.md")
	for _, required := range []string{"Reuse a verified working trigger", "legacy `EIGENFLUX_MODE`", "not a repair reason", "confirmed execution incompatibility", "Preserve task identity, thread, Home", "enabled/paused state", "Do not guess missing or conflicting modes", "Respect host approval requirements", "retain the original task", "Never restart onboarding"} {
		if !strings.Contains(reference, required) {
			t.Errorf("missing compatibility boundary %q", required)
		}
	}
	broadcast := readRepoFile(t, root, "skills/ef-broadcast/SKILL.md")
	if !strings.Contains(broadcast, "effective mode supplied by `--runtime-mode` or legacy") || strings.Contains(broadcast, "server, and explicit `--runtime-mode`") {
		t.Fatal("legacy modes must satisfy trigger validation")
	}
}
