package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAndLoadCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	creds := &Credentials{
		AccessToken: "at_test123",
		Email:       "test@example.com",
		ExpiresAt:   time.Now().Add(24 * time.Hour).UnixMilli(),
	}
	err := SaveCredentials("default", creds)
	if err != nil {
		t.Fatalf("SaveCredentials error: %v", err)
	}
	loaded, err := LoadCredentials("default")
	if err != nil {
		t.Fatalf("LoadCredentials error: %v", err)
	}
	if loaded.AccessToken != "at_test123" {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, "at_test123")
	}
	if loaded.Email != "test@example.com" {
		t.Errorf("Email = %q, want %q", loaded.Email, "test@example.com")
	}
}

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	_, err := LoadCredentials("nonexistent")
	if err == nil {
		t.Error("expected error loading nonexistent credentials")
	}
}

func TestRefreshExpiry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)

	// Seed credentials with an expiry in the past.
	old := &Credentials{
		AccessToken: "at_refresh",
		Email:       "refresh@example.com",
		ExpiresAt:   time.Now().Add(-48 * time.Hour).UnixMilli(),
	}
	if err := SaveCredentials("default", old); err != nil {
		t.Fatalf("SaveCredentials error: %v", err)
	}

	before := time.Now().UnixMilli()
	RefreshExpiry("default")
	after := time.Now().UnixMilli()

	loaded, err := LoadCredentials("default")
	if err != nil {
		t.Fatalf("LoadCredentials error: %v", err)
	}

	expectedMin := before + sessionDurationMs
	expectedMax := after + sessionDurationMs
	if loaded.ExpiresAt < expectedMin || loaded.ExpiresAt > expectedMax {
		t.Errorf("ExpiresAt = %d, want between %d and %d", loaded.ExpiresAt, expectedMin, expectedMax)
	}
	if loaded.AccessToken != "at_refresh" {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, "at_refresh")
	}
	if loaded.Email != "refresh@example.com" {
		t.Errorf("Email = %q, want %q", loaded.Email, "refresh@example.com")
	}
}

func TestRefreshExpiryNonexistent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)

	// Should not panic or create a file for a nonexistent server.
	RefreshExpiry("nonexistent")

	_, err := LoadCredentials("nonexistent")
	if err == nil {
		t.Error("expected error loading nonexistent credentials; RefreshExpiry should not create files")
	}
}

func TestSaveCredentialsAtomic(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)

	creds := &Credentials{
		AccessToken: "at_atomic",
		Email:       "atomic@example.com",
		ExpiresAt:   time.Now().Add(24 * time.Hour).UnixMilli(),
	}
	if err := SaveCredentials("default", creds); err != nil {
		t.Fatalf("SaveCredentials error: %v", err)
	}

	// Verify no temp files are left behind.
	// HomeDir appends ".eigenflux" to the EIGENFLUX_HOME value.
	entries, err := os.ReadDir(filepath.Join(dir, ".eigenflux", "servers", "default"))
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "credentials.json" {
			t.Errorf("unexpected file left behind: %s", e.Name())
		}
	}

	// Verify file permissions.
	info, err := os.Stat(filepath.Join(dir, ".eigenflux", "servers", "default", "credentials.json"))
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}

func TestIsExpired(t *testing.T) {
	expired := &Credentials{AccessToken: "at_old", ExpiresAt: time.Now().Add(-1 * time.Hour).UnixMilli()}
	if !expired.IsExpired() {
		t.Error("expected expired=true for past ExpiresAt")
	}
	valid := &Credentials{AccessToken: "at_new", ExpiresAt: time.Now().Add(24 * time.Hour).UnixMilli()}
	if valid.IsExpired() {
		t.Error("expected expired=false for future ExpiresAt")
	}
	noExpiry := &Credentials{AccessToken: "at_noexp", ExpiresAt: 0}
	if noExpiry.IsExpired() {
		t.Error("expected expired=false when ExpiresAt=0")
	}
}
