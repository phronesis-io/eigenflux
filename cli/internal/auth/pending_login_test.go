package auth

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPendingLoginRoundTrip(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	err := SavePendingLogin(&PendingLogin{
		ChallengeID: "ch_test",
		Email:       "user@example.com",
		Ref:         "EF-abc12345",
	})
	if err != nil {
		t.Fatalf("SavePendingLogin error: %v", err)
	}
	p := LoadPendingLogin("ch_test")
	if p == nil {
		t.Fatal("LoadPendingLogin returned nil for matching challenge")
	}
	if p.Email != "user@example.com" || p.Ref != "EF-abc12345" {
		t.Errorf("loaded %+v, want email/ref preserved", p)
	}
	if LoadPendingLogin("ch_other") != nil {
		t.Error("expected nil for mismatched challenge ID")
	}
	if err := DeletePendingLogin(); err != nil {
		t.Fatalf("DeletePendingLogin error: %v", err)
	}
	if LoadPendingLogin("ch_test") != nil {
		t.Error("expected nil after delete")
	}
	if err := DeletePendingLogin(); err != nil {
		t.Errorf("DeletePendingLogin on missing file should be nil, got %v", err)
	}
}

func TestPendingLoginStale(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	p := &PendingLogin{ChallengeID: "ch_old", Email: "user@example.com"}
	if err := SavePendingLogin(p); err != nil {
		t.Fatalf("SavePendingLogin error: %v", err)
	}
	p.CreatedAt = time.Now().Add(-25 * time.Hour).UnixMilli()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal stale state: %v", err)
	}
	if err := writeFileAtomic(pendingLoginPath(), data); err != nil {
		t.Fatalf("rewrite stale state: %v", err)
	}
	if LoadPendingLogin("ch_old") != nil {
		t.Error("expected nil for state older than 24h")
	}
}
