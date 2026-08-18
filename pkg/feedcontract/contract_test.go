package feedcontract

import (
	"os"
	"path/filepath"
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
