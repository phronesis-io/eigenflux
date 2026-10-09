package cmd

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestOnboardingResultAndHostRouting(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile("../../skills/ef-onboarding/references/" + path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	messages, connection, host := read("messages.md"), read("connection.md"), read("host-setup.md")
	section := func(id string) string {
		t.Helper()
		r := regexp.MustCompile(`(?s)## ` + regexp.QuoteMeta(id) + `\n(.*?)(?:\n## |$)`)
		m := r.FindStringSubmatch(messages)
		if m == nil {
			t.Fatalf("missing %s", id)
		}
		return m[1]
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
