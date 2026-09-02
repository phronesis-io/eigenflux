//go:build !windows

package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateIdentityRejectsBroadPermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	if _, _, _, err := LoadOrCreateIdentity("test"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".eigenflux", "servers", "test", "identity.json")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadOrCreateIdentity("test"); err == nil || !strings.Contains(err.Error(), "require 0600") {
		t.Fatalf("broad identity permissions error = %v", err)
	}
}
