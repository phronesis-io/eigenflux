package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestMainNoticeProbeNeverWritesConversation(t *testing.T) {
	for _, scenario := range []struct {
		name, code string
		matched    bool
	}{
		{"success", "main_notice_owner_ready", true},
		{"unsupported", "main_notice_owner_capability_unverified", false},
		{"missing-capability", "main_notice_owner_capability_unverified", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			target, owner := newNoticeOwnerFixture(t, scenario.name)
			target.HiddenInputEnabled = false
			target.Binding.MainSessionID = target.SessionID
			probe := ProbeMainNotice(context.Background(), target)
			if probe.Code != scenario.code || !probe.TransportAvailable || probe.TargetMatched != scenario.matched || probe.Enabled != scenario.matched {
				t.Fatalf("probe: %+v", probe)
			}
			calls := owner.requests()
			if len(calls) != 2 || string(calls[0]["method"]) != `"initialize"` || string(calls[1]["method"]) != `"thread-owner-discovery"` {
				t.Fatalf("probe sent non-discovery methods: %+v", calls)
			}
			if _, err := os.Stat(target.Binding.Env["EF_MAIN_NOTICE_MARKER"]); !os.IsNotExist(err) {
				t.Fatalf("probe started a reader/model process: %v", err)
			}
		})
	}
}

func TestMainNoticeProbeUnavailableOrInvalid(t *testing.T) {
	target := noticeFixtureTarget(t, "codex", "codex-success")
	target.Binding.MainSessionID = target.SessionID
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probe := ProbeMainNotice(ctx, target)
	if probe.Code != "main_notice_owner_ipc_unavailable" || probe.TransportAvailable || probe.Enabled {
		t.Fatalf("cancelled probe: %+v", probe)
	}
	if _, err := os.Stat(target.Binding.Env["EF_MAIN_NOTICE_MARKER"]); !os.IsNotExist(err) {
		t.Fatalf("cancelled probe started process: %v", err)
	}
	for _, change := range []func(*MainNoticeTarget){
		func(v *MainNoticeTarget) { v.SessionID = "" },
		func(v *MainNoticeTarget) { v.Binding.MainSessionID = "other" },
		func(v *MainNoticeTarget) { v.Binding.Host = "other" },
		func(v *MainNoticeTarget) { v.Binding.Mode = "command" },
	} {
		invalid := target
		change(&invalid)
		probe := ProbeMainNotice(context.Background(), invalid)
		if probe.Code != "main_notice_target_invalid" || probe.TransportAvailable || probe.Enabled {
			t.Fatalf("invalid probe: %+v", probe)
		}
	}
	target.Host = "other"
	if probe := ProbeMainNotice(context.Background(), target); probe.Code != "main_notice_host_unverified" || probe.Enabled {
		t.Fatalf("unsupported host: %+v", probe)
	}
	if _, err := os.Stat(target.Binding.Env["EF_MAIN_NOTICE_MARKER"]); !os.IsNotExist(err) {
		t.Fatalf("invalid probe started process: %v", err)
	}
}

// This opt-in test performs no turn or model call. It reads only an explicit
// local binding and emits fixed redacted diagnostics for the installed host.
func TestMainNoticeInstalledHostReadOnlyProbe(t *testing.T) {
	path := os.Getenv("EIGENFLUX_MAIN_NOTICE_PROBE_BINDING")
	if path == "" {
		t.Skip("set EIGENFLUX_MAIN_NOTICE_PROBE_BINDING to probe an installed host")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read explicit probe binding")
	}
	var binding Binding
	if json.Unmarshal(raw, &binding) != nil {
		t.Fatal("invalid probe binding")
	}
	probe := ProbeMainNotice(context.Background(), MainNoticeTarget{Host: binding.Host, SessionID: binding.MainSessionID, Binding: binding})
	encoded, err := json.Marshal(probe)
	if err != nil {
		t.Fatal("cannot encode redacted probe")
	}
	t.Log(string(encoded))
	if expected := strings.TrimSpace(os.Getenv("EIGENFLUX_MAIN_NOTICE_EXPECT_CODE")); expected != "" && probe.Code != expected {
		t.Fatalf("probe code %s; want %s", probe.Code, expected)
	}
}
