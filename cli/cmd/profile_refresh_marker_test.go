package cmd

import (
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
	}
}

// Same contract, the copy that ships inside the binary for hosts whose backend
// does not inject one.
func TestPromptLineInBuiltinContract(t *testing.T) {
	if !strings.Contains(output.FeedContractForTest(), output.ProfileRefreshPromptLine) {
		t.Error("built-in fallback contract does not quote the prompt line verbatim")
	}
}
