package auth

import (
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
