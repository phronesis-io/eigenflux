package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/heartbeatmigration"
	"cli.eigenflux.ai/internal/maintenance"
)

func TestCustomGuardRequiresExactFreshOwnedTask(t *testing.T) {
	home := t.TempDir()
	prompt := "Run the owned heartbeat.\n保留全部项目与投资检查。"
	fixture := func() schedulerReportInput {
		return schedulerReportInput{ObservedAt: time.Now().UnixMilli(), Inventory: heartbeatmigration.Inventory{Complete: true, Tasks: []heartbeatmigration.Task{{ID: "daily", Owner: "eigenflux", Purpose: "heartbeat", Home: home, Server: "eigenflux", ThreadID: guardTestThread, Prompt: prompt, Schedule: "every 24 hours", Status: "ACTIVE"}}}}
	}
	for name, mutate := range map[string]func(*schedulerReportInput){
		"stale":        func(in *schedulerReportInput) { in.ObservedAt = time.Now().Add(-6 * time.Minute).UnixMilli() },
		"future":       func(in *schedulerReportInput) { in.ObservedAt = time.Now().Add(time.Minute).UnixMilli() },
		"incomplete":   func(in *schedulerReportInput) { in.Complete = false },
		"failed":       func(in *schedulerReportInput) { in.ErrorCode = "scheduler_api_unavailable" },
		"missing":      func(in *schedulerReportInput) { in.Tasks = nil },
		"wrong ID":     func(in *schedulerReportInput) { in.Tasks[0].ID = "other" },
		"wrong thread": func(in *schedulerReportInput) { in.Tasks[0].ThreadID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" },
		"wrong home":   func(in *schedulerReportInput) { in.Tasks[0].Home = filepath.Join(home, "other") },
		"wrong server": func(in *schedulerReportInput) { in.Tasks[0].Server = "other" },
		"wrong owner":  func(in *schedulerReportInput) { in.Tasks[0].Owner = "other" },
		"paused":       func(in *schedulerReportInput) { in.Tasks[0].Status = "PAUSED" },
		"drift":        func(in *schedulerReportInput) { in.Tasks[0].Prompt += " extra" },
		"duplicate": func(in *schedulerReportInput) {
			other := in.Tasks[0]
			other.ID = "second"
			in.Tasks = append(in.Tasks, other)
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := fixture()
			mutate(&in)
			if _, err := codexGuardVerifiedPromptHash(in, home, "eigenflux", "42", guardTestThread, "daily", prompt); err == nil {
				t.Fatal("accepted invalid readback")
			}
		})
	}
	hash, err := codexGuardVerifiedPromptHash(fixture(), home, "eigenflux", "42", guardTestThread, "daily", prompt)
	want := sha256.Sum256([]byte(prompt))
	if err != nil || hash != hex.EncodeToString(want[:]) {
		t.Fatalf("valid readback: %s %v", hash, err)
	}
	scope := maintenance.Scope{Home: home, Server: "eigenflux", AgentID: "42"}
	for _, tc := range []struct {
		turn, text string
		admitted   bool
	}{
		{"exact", prompt, true},
		{"quoted", "Explain this: " + prompt, false},
		{"fragment", "保留全部项目与投资检查。", false},
	} {
		in := heartbeatHostInput{heartbeatStopInput: heartbeatStopInput{SessionID: guardTestThread, TurnID: tc.turn, Event: "UserPromptSubmit"}, Prompt: tc.text, ExpectedPromptHash: hash}
		if err := observeHeartbeatHost(scope, in, guardTestThread); err != nil {
			t.Fatal(err)
		}
		if got := heartbeatHostRunAdmitted(scope, guardTestThread, tc.turn); got != tc.admitted {
			t.Fatalf("%s admitted=%v", tc.turn, got)
		}
	}
	cli := filepath.Join(home, "eigenflux")
	if _, err := codexGuardPromptHash(cli, home, "eigenflux", prompt); err == nil {
		t.Fatal("bare custom prompt bypassed readback")
	}
	codexHome := filepath.Join(home, "codex")
	for i := 0; i < 2; i++ {
		changed, err := installCodexGuardWithPromptHash(codexHome, cli, home, "eigenflux", guardTestThread, "42", hash)
		if err != nil || changed != (i == 0) {
			t.Fatalf("install %d: %v %v", i, changed, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(codexHome, "hooks.json"))
	if err != nil || !strings.Contains(string(raw), hash) {
		t.Fatalf("missing full prompt fence: %v", err)
	}
	path := filepath.Join(home, "readback.json")
	b, _ := json.Marshal(fixture())
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := codexGuardReadbackPromptHash(path, home, "eigenflux", "42", guardTestThread, "daily", prompt)
	if err != nil || got != hash {
		t.Fatalf("file readback: %s %v", got, err)
	}
	for _, invalid := range []string{string(b) + " {}", `{"unknown":true}`, strings.Repeat(" ", 2<<20)} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := codexGuardReadbackPromptHash(path, home, "eigenflux", "42", guardTestThread, "daily", prompt); err == nil {
			t.Fatal("accepted invalid input file")
		}
	}
}
