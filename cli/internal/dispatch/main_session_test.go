package dispatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mainTestID = "01a0d200-9d41-7cd2-9214-3f04761910c4"

func TestMainSessionLocalMetadata(t *testing.T) {
	id, home := os.Getenv("EIGENFLUX_TEST_CODEX_SESSION"), os.Getenv("EIGENFLUX_TEST_CODEX_HOME")
	if id == "" || home == "" {
		t.Skip("explicit local Codex metadata smoke test is not configured")
	}
	b := Binding{Mode: "native", Host: "codex", MainSessionID: id, Env: map[string]string{"CODEX_HOME": home}}
	if err := b.ValidateMainSession(); err != nil {
		t.Fatal(err)
	}
}

func mainSessionFixture(t *testing.T) Binding {
	t.Helper()
	b := validTestBinding(t)
	b.Mode, b.Host, b.MainSessionID = "native", "codex", mainTestID
	b.Env = map[string]string{"CODEX_HOME": t.TempDir()}
	dir := filepath.Join(b.Env["CODEX_HOME"], "sessions", "2026", "09", "24")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-date-"+mainTestID+".jsonl"), []byte(`{"type":"session_meta","payload":{"id":"`+mainTestID+`"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMainSessionRequiresExactExistingCodexThread(t *testing.T) {
	b := mainSessionFixture(t)
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Binding){
		func(b *Binding) { b.MainSessionID = "latest" },
		func(b *Binding) { b.MainSessionID = "01a0d200-9d41-7cd2-9214-3f04761910c5" },
		func(b *Binding) { b.Host = "workbuddy" },
		func(b *Binding) { b.Mode = "acp" },
		func(b *Binding) { b.Env = nil },
		func(b *Binding) { b.Env = map[string]string{"CODEX_HOME": "relative"} },
	} {
		candidate := b
		change(&candidate)
		if err := candidate.ValidateMainSession(); err == nil {
			t.Fatalf("accepted unsupported target: %+v", candidate)
		}
	}
	if err := os.Rename(filepath.Join(b.Env["CODEX_HOME"], "sessions"), filepath.Join(b.Env["CODEX_HOME"], "archived_sessions")); err != nil {
		t.Fatal(err)
	}
	if err := b.ValidateMainSession(); !errors.Is(err, ErrMainSessionUnavailable) {
		t.Fatalf("archived session accepted: %v", err)
	}
}

func TestMainSessionReplacementChecksPreviousBinding(t *testing.T) {
	b := mainSessionFixture(t)
	b.MainSessionID = ""
	got, err := SetMainSession(b, b.Revision, "", mainTestID)
	if err != nil || got.MainSessionID != mainTestID || got.Revision != b.Revision {
		t.Fatalf("update: %+v %v", got, err)
	}
	if _, err = SetMainSession(got, b.Revision, "", mainTestID); err == nil {
		t.Fatal("stale previous ID accepted")
	}
	if _, err = SetMainSession(got, strings.Repeat("c", 32), mainTestID, mainTestID); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, err = SetMainSession(got, b.Revision, mainTestID, ""); err == nil {
		t.Fatal("empty replacement accepted")
	}
}

func TestACPMainBindingUsesOpaqueExplicitOwnerID(t *testing.T) {
	for _, host := range []string{"claude-code", "openclaw", "hermes", "workbuddy", "third-party"} {
		b := validTestBinding(t)
		b.Host, b.Mode, b.MainSessionID = host, "acp", "agent:explicit-eigenflux:main"
		if err := b.Validate(); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		updated, err := SetMainSession(b, b.Revision, b.MainSessionID, "new-explicit-owner-id")
		if err != nil || updated.Revision != b.Revision || updated.MainSessionID != "new-explicit-owner-id" {
			t.Fatalf("%s: %+v %v", host, updated, err)
		}
		for _, invalid := range []string{"--last", " main", "main\n", "\x00", strings.Repeat("x", 4097)} {
			b.MainSessionID = invalid
			if b.ValidateMainSession() == nil {
				t.Fatalf("%s accepted invalid owner ID %q", host, invalid)
			}
		}
	}
}

func TestMainSessionRunnerNeverFallsBack(t *testing.T) {
	b := mainSessionFixture(t)
	r := Runner{Binding: b}
	if _, err := r.Run(context.Background(), Request{Kind: "pm_push", SessionID: "old-peer-session"}); !errors.Is(err, ErrMainSessionMismatch) {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(b.Env["CODEX_HOME"], "sessions")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), Request{Kind: "pm_push", SessionID: mainTestID}); !errors.Is(err, ErrMainSessionUnavailable) {
		t.Fatal(err)
	}
}

func TestMainSessionRunnerVerifiesNativeReceipt(t *testing.T) {
	for _, scenario := range []string{"codex-main", "codex-wrong-main"} {
		b := mainSessionFixture(t)
		b.Command = append(b.Command, "-test.run=^TestRunnerFixture$", "--")
		b.Env["EF_RUNNER_FIXTURE"] = scenario
		result, err := (Runner{Binding: b}).runCodexQueue(context.Background(), Request{Kind: "pm_push", SessionID: mainTestID, Prompt: "test"})
		if scenario == "codex-main" {
			if err != nil || result.SessionID != mainTestID {
				t.Fatalf("%+v %v", result, err)
			}
		} else if !errors.Is(err, ErrMainSessionMismatch) {
			t.Fatalf("wrong native session accepted: %+v %v", result, err)
		}
	}
}
