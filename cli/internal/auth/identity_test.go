package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateIdentityIsStableAndPrivate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	publicOne, privateOne, created, err := LoadOrCreateIdentity("test")
	if err != nil || !created {
		t.Fatalf("first identity create: created=%v err=%v", created, err)
	}
	publicTwo, privateTwo, createdAgain, err := LoadOrCreateIdentity("test")
	if err != nil || createdAgain {
		t.Fatalf("second identity load: created=%v err=%v", createdAgain, err)
	}
	if !bytes.Equal(publicOne, publicTwo) || !bytes.Equal(privateOne, privateTwo) {
		t.Fatal("identity changed across repeated loads")
	}
	path := filepath.Join(dir, ".eigenflux", "servers", "test", "identity.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("identity mode=%o want=600", info.Mode().Perm())
	}
	if IdentityFingerprint(publicOne) == "" {
		t.Fatal("empty identity fingerprint")
	}
}

func TestLoadOrCreateIdentityRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	identityDir := filepath.Join(dir, ".eigenflux", "servers", "test")
	if err := os.MkdirAll(identityDir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(identityDir, "identity.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, _, err := LoadOrCreateIdentity("test"); err == nil {
		t.Fatal("symlink identity path was accepted")
	}
}
