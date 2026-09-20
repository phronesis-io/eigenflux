package feedcontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contract.md")
	if err := os.WriteFile(path, []byte("  contract body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := Load(path); got != "contract body" {
		t.Fatalf("Load()=%q", got)
	}
}

func TestLoadFindsRelativeContractFromNestedWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	contractDir := filepath.Join(root, "static")
	if err := os.MkdirAll(contractDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contractDir, "feed_contract.md"), []byte("nested contract\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "api", "consolev2")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	if got := Load("static/feed_contract.md"); got != "nested contract" {
		t.Fatalf("Load()=%q", got)
	}
}

func TestForModeMatchesGeneratedSkills(t *testing.T) {
	for _, tc := range []struct{ mode, source, generated string }{
		{"baseline", "baseline-contract.md", "feed_baseline_contract.md"},
		{"intent_aligned", "contract.md", "feed_contract.md"},
		{"", "contract.md", "feed_contract.md"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "..", "skills", "ef-broadcast", "references", tc.source))
			if err != nil {
				t.Fatal(err)
			}
			want := strings.TrimSpace(string(source))
			if want == "" {
				t.Fatal("Skill contract must not be empty")
			}
			if got := Load(filepath.Join("..", "..", "static", tc.generated)); got != want {
				t.Fatalf("%s is out of sync with %s", tc.generated, tc.source)
			}
			if got := ForMode(tc.mode); got != want {
				t.Fatalf("ForMode(%q) did not select %s", tc.mode, tc.source)
			}
		})
	}
}

func TestBaselineContractKeepsIncompleteOnboardingReadOnly(t *testing.T) {
	contract := ForMode("baseline")
	for _, required := range []string{
		"READ-ONLY OUTPUT CONTRACT",
		"Keep the recurring heartbeat active",
		"Surface relevant items",
		"Use the supplied preview",
		"NO_REPLY",
		"skip feedback, behavior-event writes, private messages, friend operations, publishing, profile changes, and Active Attention",
		"Feed has no delivery ACK",
		"Upload Attention Prefill only within an explicit onboarding or upgrade flow",
		"Continue available Feed reads",
	} {
		if !strings.Contains(contract, required) {
			t.Errorf("baseline contract is missing %q", required)
		}
	}
	for _, completedOnly := range []string{
		"eigenflux feedback", "eigenflux events", "eigenflux publish", "eigenflux msg",
		"eigenflux relation", "eigenflux profile", "eigenflux settings push",
		"eigenflux feed get", "[PENDING TASK]", "Submit feedback", "submit feedback",
	} {
		if strings.Contains(contract, completedOnly) {
			t.Errorf("baseline contract must not require completed-only work: %q", completedOnly)
		}
	}
}
