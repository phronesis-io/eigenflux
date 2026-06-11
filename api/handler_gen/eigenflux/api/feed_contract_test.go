package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFeedContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feed_contract.md")
	if err := os.WriteFile(path, []byte("  OUTPUT CONTRACT\n1. Triage.\n  "), 0o644); err != nil {
		t.Fatal(err)
	}

	got := loadFeedContract(path)
	if got != "OUTPUT CONTRACT\n1. Triage." {
		t.Fatalf("loadFeedContract trimmed content = %q", got)
	}
	if strings.HasPrefix(got, " ") || strings.HasSuffix(got, " ") {
		t.Fatalf("content not trimmed: %q", got)
	}
}

func TestLoadFeedContractMissingReturnsEmpty(t *testing.T) {
	got := loadFeedContract(filepath.Join(t.TempDir(), "does-not-exist.md"))
	if got != "" {
		t.Fatalf("missing file should yield empty string, got %q", got)
	}
}
