package cmd

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Render the actual production fragments across both profile and browser paths.
// Native tool selection and visible opening still require a desktop walkthrough.
func TestOnboardingBrowserHandoffRendering(t *testing.T) {
	b, err := os.ReadFile("../../skills/ef-onboarding/references/messages.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, newline := range []string{"\n", "\r\n"} {
		messages := strings.ReplaceAll(string(b), "\n", newline)
		fragment := func(id, locale string) string {
			t.Helper()
			normalized := strings.ReplaceAll(messages, "\r\n", "\n")
			section := regexp.MustCompile(`(?s)\n## ` + regexp.QuoteMeta(id) + `\n(.*?)(?:\n## |$)`).FindStringSubmatch(normalized)
			if section == nil {
				t.Fatalf("missing fragment %s", id)
			}
			parts := strings.SplitN(section[1], "### "+locale+"\n", 2)
			if len(parts) != 2 {
				t.Fatalf("missing locale %s/%s", id, locale)
			}
			body := strings.SplitN(parts[1], "\n### ", 2)[0]
			var lines []string
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(line, ">") {
					lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, ">"), " "))
				}
			}
			return strings.Join(lines, "\n")
		}
		for _, locale := range []string{"zh", "en"} {
			for _, profile := range []string{"profile_ready", "profile_manual"} {
				for _, browser := range []string{"browser_opened", "browser_external_requested", "browser_manual"} {
					t.Run(locale+"/"+profile+"/"+browser+"/"+map[string]string{"\n": "LF", "\r\n": "CRLF"}[newline], func(t *testing.T) {
						// Non-default origin/port and encoded ticket/nonce catch lossy URL rendering.
						url := "https://console.example.test:8443/dashboard/handoff?ticket=efht_A%2BB&source=test#nonce=N%2Fz%3D"
						if err := validateConsoleHandoffURL(url); err != nil {
							t.Fatal(err)
						}
						body := strings.NewReplacer(
							"<profile-result>", fragment(profile, locale),
							"<browser-instruction>", fragment(browser, locale),
							"<return-instruction>", strings.ReplaceAll(fragment("return_host", locale), "<first-check-request>", fragment("first_check_request", locale)),
							"<progress>", "stage-4-pending",
							"<console-url>", url,
						).Replace(fragment("handoff", locale))
						links := regexp.MustCompile(`\]\(([^\n]+)\)`).FindAllStringSubmatch(body, -1)
						if len(links) != 1 || links[0][1] != url {
							t.Fatalf("handoff must preserve exactly one complete link: %q", body)
						}
						if regexp.MustCompile(`<[-a-z]+>`).MatchString(body) {
							t.Fatalf("unresolved handoff slot: %q", body)
						}
						if !strings.Contains(body, fragment("first_check_request", locale)) {
							t.Fatal("browser opening must retain the explicit first-check request")
						}
						if browser == "browser_manual" && (strings.Contains(body, "I've opened") || strings.Contains(body, "已在内置浏览器")) {
							t.Fatal("manual fallback must not claim an opened browser")
						}
					})
				}
			}
		}
	}
}

func TestOnboardingResultAndHostRouting(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(map[string]string{"\n": "LF", "\r\n": "CRLF"}[newline], func(t *testing.T) {
			testOnboardingResultAndHostRouting(t, newline)
		})
	}
}

func testOnboardingResultAndHostRouting(t *testing.T, newline string) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile("../../skills/ef-onboarding/references/" + path)
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n", newline)
	}
	messages, connection, host := read("messages.md"), read("connection.md"), read("host-setup.md")
	section := func(id string) string {
		t.Helper()
		r := regexp.MustCompile(`(?s)## ` + regexp.QuoteMeta(id) + `\n(.*?)(?:\n## |$)`)
		m := r.FindStringSubmatch(strings.ReplaceAll(messages, "\r\n", "\n"))
		if m == nil {
			t.Fatalf("missing %s", id)
		}
		return m[1]
	}
	for _, locale := range []string{"zh", "en"} {
		body := strings.SplitN(strings.SplitN(section("execution"), "### "+locale+"\n", 2)[1], "\n### ", 2)[0]
		if strings.Count(body, "> <permission-scope>") != 1 {
			t.Errorf("execution/%s must render the shared Codex permission scope exactly once", locale)
		}
	}
	empty := section("check_empty")
	if strings.Count(empty, "> <empty-actions>") != 2 || strings.Count(empty, "> <followup>") != 2 {
		t.Fatal("both empty-result languages must inline actions and actual scheduler state")
	}
	for _, required := range []string{"Never append `action_menu` after", "`check_empty`", "do not fetch another Feed", "draft approval"} {
		if !strings.Contains(connection, required) {
			t.Fatalf("missing result boundary %s", required)
		}
	}
	consent := section("execution_generic")
	consent = consent[strings.Index(consent, "### zh"):]
	for _, forbidden := range []string{"Codex", "restart", "sandbox.orderedRules", "permissions.allow"} {
		if strings.Contains(consent, forbidden) {
			t.Fatalf("Generic host user copy leaks %s", forbidden)
		}
	}
	if !strings.Contains(host, "ask `execution_generic`") || strings.Contains(messages, "## execution_workbuddy") {
		t.Fatal("non-Codex hosts must use the generic permission route")
	}
	for _, id := range []string{"empty_broadcast", "empty_need", "empty_action_reply", "empty_explore", "execution_generic"} {
		section(id)
	}
	if regexp.MustCompile(`(?m)^> <[a-z-]+$`).MatchString(messages) {
		t.Fatal("truncated template slot")
	}
}

func TestRichPrefillAndFeedPresentationContract(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile("../../" + path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	prefill := read("skills/ef-onboarding/references/prefill.md")
	for _, phrase := range []string{"up to 10 distinct, evidence-backed", "not a quota", "approved scope", "Never infer permission", "network_action", "trade_action", "Unsupported strings stay empty", "identifying combinations", "provenance"} {
		if !strings.Contains(prefill, phrase) {
			t.Errorf("missing prefill safeguard: %s", phrase)
		}
	}
	if strings.Contains(prefill, "Derive 1–3") {
		t.Error("prefill must not artificially cap supported intents at three")
	}
	contract := read("skills/ef-broadcast/references/contract.md")
	if contract != read("static/feed_contract.md") {
		t.Error("static feed contract differs from canonical Skill")
	}
	for _, path := range []string{"skills/ef-broadcast/references/contract.md", "skills/ef-broadcast/references/feed.md"} {
		body := read(path)
		for _, phrase := range []string{"faithful source excerpts", "mark it as a summary", "outside the blockquote", "without a \"My analysis\" heading", "concrete", "4000"} {
			if !strings.Contains(body, phrase) {
				t.Errorf("%s missing presentation boundary %s", path, phrase)
			}
		}
	}
}

func TestOnboardingCommunityInvitation(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile("../../skills/ef-onboarding/" + path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	connection := read("references/connection.md")
	for _, phrase := range []string{"after `action_menu`", "`check_empty`", "Do not wait for an action selection", "Do not repeat it in action results", "original task history", "scheduled heartbeat", "Never join a community", "absolute path", "show Discord only"} {
		if !strings.Contains(connection, phrase) {
			t.Errorf("missing community boundary: %s", phrase)
		}
	}
	messages := read("references/messages.md")
	if strings.Count(messages, "https://discord.gg/MWFr98Pbe") != 2 {
		t.Error("expected bilingual community links")
	}
	if !strings.HasPrefix(read("assets/wechat-group-qr.png"), "\x89PNG\r\n\x1a\n") {
		t.Error("missing bundled PNG")
	}
}

func TestOnboardingPrefillContextDiscovery(t *testing.T) {
	body, err := os.ReadFile("../../skills/ef-onboarding/references/prefill.md")
	if err != nil {
		t.Fatal(err)
	}
	prefill := strings.ReplaceAll(string(body), "\r\n", "\n")
	for _, phrase := range []string{
		"After affirmative Prefill consent", "tool declarations", "tool\ndiscovery",
		"`list_threads`", "`read_thread`", "unless the user limited that scope",
		"task titles alone\nare not profile evidence", "before consent",
		"uninspected sources", "denied or failed", "successful retrieval with no relevant evidence",
		"without bypassing them through raw session files", "On the manual path, skip retrieval and inference",
	} {
		if !strings.Contains(prefill, phrase) {
			t.Errorf("missing context discovery boundary: %s", phrase)
		}
	}
	host, err := os.ReadFile("../../skills/ef-onboarding/references/host-setup.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(host), "`prefill_choice` for every user") {
		t.Fatal("context discovery must preserve the common optional Prefill question")
	}
}
