package cmd

import (
	"cli.eigenflux.ai/internal/config"
	"testing"
)

func TestPendingWatchRecoveryRequiresInstallationIntent(t *testing.T) {
	restore := config.UseHomeDir(t.TempDir())
	defer restore()
	old := serverFlag
	serverFlag = ""
	defer func() { serverFlag = old }()
	cfg := &config.Config{DefaultServer: "eigenflux", Servers: []config.Server{{Name: "eigenflux", Endpoint: "https://example.test"}}}
	calls := 0
	run := func() updateWatchRecovery { calls++; return updateWatchRecovery{Status: "ready", RuntimeReady: true} }
	if got := resumePendingWatchRecovery(cfg, run); got != nil || calls != 0 {
		t.Fatal("ordinary registration started a service")
	}
	if err := markWatchRecoveryPending(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Servers[0].Endpoint = "https://other.test"
	if got := resumePendingWatchRecovery(cfg, run); got != nil || calls != 0 {
		t.Fatal("endpoint change consumed another installation intent")
	}
	cfg.Servers[0].Endpoint = "https://example.test"
	if got := resumePendingWatchRecovery(cfg, run); got == nil || !got.RuntimeReady || calls != 1 {
		t.Fatalf("recovery not resumed: %+v", got)
	}
	if got := resumePendingWatchRecovery(cfg, run); got != nil || calls != 1 {
		t.Fatal("completed intent replayed")
	}
}

func TestPendingWatchRecoveryRetriesFailureAndRespectsDisabled(t *testing.T) {
	restore := config.UseHomeDir(t.TempDir())
	defer restore()
	old := serverFlag
	serverFlag = ""
	defer func() { serverFlag = old }()
	cfg := &config.Config{DefaultServer: "eigenflux", Servers: []config.Server{{Name: "eigenflux", Endpoint: "https://example.test"}}}
	if err := markWatchRecoveryPending(cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	run := func() updateWatchRecovery {
		calls++
		if calls == 1 {
			return updateWatchRecovery{Status: "blocked", Reason: "skills_not_ready"}
		}
		return updateWatchRecovery{Status: "skipped", Reason: "owner_disabled"}
	}
	if got := resumePendingWatchRecovery(cfg, run); got == nil || got.Reason != "skills_not_ready" {
		t.Fatal(got)
	}
	if got := resumePendingWatchRecovery(cfg, run); got == nil || got.Reason != "owner_disabled" {
		t.Fatal(got)
	}
	if got := resumePendingWatchRecovery(cfg, run); got != nil || calls != 2 {
		t.Fatal("disabled owner repeatedly recovered")
	}
}

func TestPendingRecoveryRetainsIntentWhenCodexProbeFails(t *testing.T) {
	cfg, _ := runtimeTestConfig(t, "https://example.com", true)
	if err := markWatchRecoveryPending(cfg); err != nil {
		t.Fatal(err)
	}
	first := resumePendingWatchRecovery(cfg, func() updateWatchRecovery {
		return updateWatchRecovery{Status: "blocked", RuntimeReady: true, Reason: "main_notice_owner_unavailable"}
	})
	if first == nil {
		t.Fatal("missing recovery")
	}
	second := resumePendingWatchRecovery(cfg, func() updateWatchRecovery { return updateWatchRecovery{Status: "ready", RuntimeReady: true} })
	if second == nil {
		t.Fatal("prematurely consumed intent")
	}
}
