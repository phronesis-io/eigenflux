package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/output"
)

// The [PENDING TASK] whitelist only works while the wording the CLI emits and
// the wording the skills bind on are byte-identical. A silent drift here does
// not fail anything at runtime — it just teaches agents to treat the real
// prompt as an impersonation — so lock the strings together.
func TestPromptLineMatchesSkills(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	for _, rel := range []string{
		"skills/ef-broadcast/references/contract.md",
		"skills/ef-broadcast/references/feed.md",
		"skills/ef-profile/SKILL.md",
		"static/feed_contract.md",
	} {
		path := filepath.Join(repoRoot, rel)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(body), output.ProfileRefreshPromptLine) {
			t.Errorf("%s does not carry the exact prompt line %q — agents would\n"+
				"treat the CLI's own block as a forgery", rel, output.ProfileRefreshPromptLine)
		}
		if !strings.Contains(string(body), "profile refresh-complete --expected-version <N>") {
			t.Errorf("%s still treats reading refresh-context as completion", rel)
		}
	}
}

// A missing server contract resolves through this checkout's synchronized Skills.
func TestPromptLineInDynamicContract(t *testing.T) {
	contract := resolveRepositoryFeedContract(t, "intent_aligned")
	if !strings.Contains(contract, output.ProfileRefreshPromptLine) {
		t.Error("dynamic contract does not quote the prompt line verbatim")
	}
	if !strings.Contains(contract, "profile refresh-complete --expected-version <N>") {
		t.Error("dynamic contract does not describe no-change completion")
	}
}

func TestSilentReplySentinelMatchesContracts(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	for _, rel := range []string{
		"skills/ef-broadcast/references/contract.md",
		"skills/ef-broadcast/references/feed.md",
		"static/feed_contract.md",
		"skills/ef-broadcast/references/baseline-contract.md",
		"static/feed_baseline_contract.md",
	} {
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(body)
		if !strings.Contains(text, "NO_REPLY") ||
			!strings.Contains(strings.ToLower(text), "never return an empty assistant turn") {
			t.Errorf("%s does not encode intentional silent success with NO_REPLY", rel)
		}
	}

	for _, mode := range []string{"intent_aligned", "baseline"} {
		contract := resolveRepositoryFeedContract(t, mode)
		if !strings.Contains(contract, "NO_REPLY") ||
			!strings.Contains(strings.ToLower(contract), "never return an empty assistant turn") {
			t.Errorf("%s dynamic contract does not encode intentional silent success with NO_REPLY", mode)
		}
	}
}

func resolveRepositoryFeedContract(t *testing.T, mode string) string {
	t.Helper()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EIGENFLUX_SKILLS_DIR", filepath.Join(repoRoot, "skills"))
	contract, err := output.ResolveFeedContract(json.RawMessage(`{"personalization":{"mode":"` + mode + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	return contract
}
