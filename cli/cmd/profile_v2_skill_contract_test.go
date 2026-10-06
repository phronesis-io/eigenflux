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
		"Do not use the onboarding handoff template",
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
	if !strings.Contains(frontmatter[1], `version: "0.9.9"`) {
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
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	requiredByFile := map[string][]string{
		"SKILL.md": {
			"references/messages.md", "references/host-setup.md", "references/prefill.md", "references/connection.md",
			"Treat\nincomplete V2 setup as existing-account maintenance", "unless the user explicitly",
			"https://cdn.eigenflux.ai/skills/latest/install.md#verify-and-continue",
			"CLI 0.0.54+", "current-host\n   installation verification", "bare-CLI setup",
			"No answer grants no permission", "Generic continuation grants no missing consent",
			"natural-language", "host-native\nexecution approvals remain separate",
			"never derive identity from cwd", "not historical onboarding text in a scheduled",
		},
		"references/prefill.md": {
			"manual path", "field_provenance", "agent_user_context", "agent_inferred",
			"`working_languages` protocol accepts only `zh` and `en`",
			"Never infer permission\nfor `network_action` or `trade_action`",
			"never include names, emails, credentials, internal URLs",
			"Do not enumerate, summarize, quote, or ask the user",
			"On the personalized path, `agent_name` must be non-empty",
			"The manual path keeps `agent_name` empty", "At most 10 intent actions",
		},
		"references/connection.md": {
			"eigenflux --homedir \"<agent-home>\" agent init --format json",
			"--draft-json '<draft-json>'", "--require-existing-agent", "--recover-account",
			"a non-empty `ticket` query parameter", "a non-empty `nonce` URL fragment",
			"The Console always opens at Step 1", "An Agent ID change is not a reason to call provision again",
			"`schema_version: feed.v2`", "`personalization.mode: baseline`",
			"Do not load `ef-broadcast` as a whole", "does\nnot authorize or perform Feed feedback",
			"zero qualified items skips the upload and is a valid", "at most\n10 for Attention Prefill",
			"heartbeat plan --format json", "A failed command/query is an\nunknown state",
			"plan itself does\nnot execute the cycle", "Never repeat completed mutations",
			"Never show this menu in automatic checks", "menu is not a sixth stage",
		},
	}
	for rel, fragments := range requiredByFile {
		body := readRepoFile(t, root, "skills/ef-onboarding/"+rel)
		for _, fragment := range fragments {
			if !strings.Contains(body, fragment) {
				t.Errorf("%s missing boundary %q", rel, fragment)
			}
		}
	}
}

func TestConsoleV2SchedulerPromptMatchesCLI(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	reference := readRepoFile(t, repoRoot, "skills/ef-onboarding/references/host-setup.md")
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
		"skills/ef-broadcast/SKILL.md":       {"ef-onboarding/references/host-setup.md"},
		"skills/ef-communication/SKILL.md":   {"ef-onboarding/references/host-setup.md"},
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
		"verified-components overview and scheduled-check\nquestion",
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
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	entry := readRepoFile(t, root, "skills/ef-onboarding/SKILL.md")
	previous := -1
	for _, stage := range []string{"**Components.**", "**Permissions.**", "**Profile preparation.**", "**Website setup.**", "**First check.**"} {
		index := strings.Index(entry, stage)
		if index <= previous {
			t.Fatalf("stage missing/out of order: %s", stage)
		}
		previous = index
	}
	host := readRepoFile(t, root, "skills/ef-onboarding/references/host-setup.md")
	for _, boundary := range []string{
		"Scheduling and execution permission are required separately. Prefill is optional",
		"one read-only initial network check", "Do not create the trigger yet",
		"without another consent question or duplicate write",
		"never replace a conflicting `prompt` or `forbidden`", "A denied write does not",
		"Read the file back and check", "trailing flags can override the",
		"one full\nquit and reopen", "original\ntask's confirmed tool history",
		"cannot prove a process reload", "Do not block on such an unobservable",
		"Generic continuation grants no missing Rules", "A user-disabled trigger is",
		"same Home, explicit server", "Do not branch on context availability",
	} {
		if !strings.Contains(host, boundary) {
			t.Errorf("missing host boundary %q", boundary)
		}
	}
	for _, obsolete := range []string{"consent.md", "execution-permission.md", "activation.md", "console-handoff.md"} {
		if _, err := os.Stat(filepath.Join(root, "skills/ef-onboarding/references", obsolete)); !os.IsNotExist(err) {
			t.Errorf("obsolete reference remains: %s", obsolete)
		}
		// Production references must not point back to removed owners.
		walkErr := filepath.WalkDir(filepath.Join(root, "skills"), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(path, ".md") {
				body, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				if strings.Contains(string(body), obsolete) {
					t.Errorf("stale reference %s in %s", obsolete, path)
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatal(walkErr)
		}
	}
}

func TestOnboardingFixedTemplateCoverage(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	messages := readRepoFile(t, root, "skills/ef-onboarding/references/messages.md")
	allowed := map[string]bool{"<current-permission>": true, "<host>": true, "<cadence>": true, "<permission-scope>": true,
		"<progress>": true, "<profile-result>": true, "<return-instruction>": true, "<console-url>": true,
		"<followup>": true, "<results>": true, "<failure>": true, "<next-action>": true,
		"<first-check-request>": true, "<action-options>": true, "<direction>": true,
		"<item-title>": true, "<topic>": true, "<need-goal>": true}
	slots := regexp.MustCompile(`<[^>]+>`)
	seen := map[string]bool{}
	for _, section := range strings.Split(messages, "\n## ")[1:] {
		id := strings.SplitN(section, "\n", 2)[0]
		if seen[id] {
			t.Errorf("duplicate template %s", id)
		}
		seen[id] = true
		for _, locale := range []string{"zh", "en"} {
			pieces := strings.Split(section, "### "+locale+"\n")
			if len(pieces) != 2 {
				t.Fatalf("%s has no unique %s body", id, locale)
			}
			body := strings.SplitN(pieces[1], "\n### ", 2)[0]
			if !strings.Contains(body, "\n> ") {
				t.Errorf("%s/%s empty", id, locale)
			}
			for _, slot := range slots.FindAllString(body, -1) {
				if !allowed[slot] {
					t.Errorf("%s/%s undeclared slot %s", id, locale, slot)
				}
			}
			if strings.Contains(body, "prefix_rule") || strings.Contains(body, "rules-file") {
				t.Errorf("%s/%s leaked internal permission details", id, locale)
			}
		}
	}
	for _, id := range []string{"welcome", "schedule", "execution", "scope_codex", "execution_existing", "paused", "restart",
		"prefill_choice", "profile_ready", "profile_manual", "handoff", "return_host", "return_website",
		"first_check_request", "action_peers", "action_detail", "action_broadcast", "action_need",
		"website_incomplete", "check_start", "check_done", "check_empty", "followup_active", "followup_paused", "followup_unknown", "action_menu", "check_unavailable", "check_failed", "setup_failed", "preparing_draft", "preparing_manual", "preparing_network", "link_refreshed", "clarify_permission"} {
		if !seen[id] {
			t.Errorf("missing template %s", id)
		}
	}
	// Required choices must be short, distinct and owned only by messages.md.
	for _, choice := range []string{"*同意*", "*不同意，先停止接入*", "*Agree*", "*Decline, stop connecting for now*"} {
		if strings.Count(messages, "> - "+choice) != 2 {
			t.Errorf("missing/duplicated required choice %s", choice)
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
	reference := readRepoFile(t, root, "skills/ef-onboarding/references/host-setup.md")
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

func TestFirstCheckOutputContract(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	contract := readRepoFile(t, root, "skills/ef-broadcast/references/contract.md")
	delivered := readRepoFile(t, root, "static/feed_contract.md")
	if contract != delivered {
		t.Fatal("server-delivered Feed contract is stale")
	}
	feed := readRepoFile(t, root, "skills/ef-broadcast/references/feed.md")
	for name, text := range map[string]string{"contract": contract, "feed": feed} {
		for _, boundary := range []string{"current human-requested first check routed through `ef-onboarding`", "successful-empty confirmation", "only after the whole foreground check succeeds", "Scheduled runs never inherit this exception"} {
			if !strings.Contains(text, boundary) {
				t.Errorf("%s missing first-check output boundary %q", name, boundary)
			}
		}
	}
}

func TestReleasedCLIOnboardingSchedulerReferenceResolves(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	migration := schedulerMigrationForRuntime("codex", "skill", "eigenflux --homedir '/tmp/nondefault home' heartbeat plan --format agent")
	// Follow the filename actually emitted by the unchanged CLI, rather than
	// assuming that deployed versions know the reorganized Skill layout.
	match := regexp.MustCompile(`Follow ([a-z-]+\.md)`).FindStringSubmatch(migration)
	if len(match) != 2 {
		t.Fatalf("no scheduler reference in %q", migration)
	}
	reference := readRepoFile(t, root, "skills/ef-onboarding/references/"+match[1])
	link := regexp.MustCompile(`\]\(([^)#]+)(?:#[^)]*)?\)`).FindStringSubmatch(reference)
	if len(link) != 2 {
		t.Fatal("released CLI reference has no canonical destination")
	}
	canonical := readRepoFile(t, root, "skills/ef-onboarding/references/"+link[1])
	if !strings.Contains(canonical, heartbeatSchedulerPrompt("<launcher>")) {
		t.Fatal("compatibility pointer does not reach the scheduler prompt owner")
	}
	if strings.Contains(reference, "```") {
		t.Fatal("compatibility entry duplicated a procedure instead of forwarding")
	}
}
