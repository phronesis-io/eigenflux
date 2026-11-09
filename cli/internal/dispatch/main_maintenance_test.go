package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaintenanceRejectsUnboundOrInvalidInputBeforeProcessStart(t *testing.T) {
	for _, scenario := range []string{"disabled", "host", "binding-host", "mode", "no-main", "changed-main", "relative-home", "no-home", "empty-rules", "invalid-rules", "oversize", "invalid-json", "invalid-json-utf8", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			target := noticeFixtureTarget(t, "codex", "unused")
			target.Binding.MainSessionID = target.SessionID
			target.Binding.Env["CODEX_HOME"] = t.TempDir()
			application, data := noticeCodexRules, noticeCodexData
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "disabled":
				target.HiddenInputEnabled = false
			case "host":
				target.Host = "hermes"
			case "binding-host":
				target.Binding.Host = "hermes"
			case "mode":
				target.Binding.Mode = "acp"
			case "no-main":
				target.Binding.MainSessionID = ""
			case "changed-main":
				target.SessionID = "other-session"
			case "relative-home":
				target.Binding.Env["CODEX_HOME"] = "relative"
			case "no-home":
				delete(target.Binding.Env, "CODEX_HOME")
			case "empty-rules":
				application = " \n "
			case "invalid-rules":
				application = string([]byte{0xff})
			case "oversize":
				application = strings.Repeat("x", maxRunnerOutput)
			case "invalid-json":
				data = "{} {}"
			case "invalid-json-utf8":
				data = "\"" + string([]byte{0xff}) + "\""
			case "canceled":
				cancel()
			}
			result, err := (MainNoticeRunner{Target: target}).RunMaintenance(ctx, application, data)
			if err == nil || result != (Result{}) || MainNoticeCanRetry(err) {
				t.Fatalf("invalid input admitted: %+v %v", result, err)
			}
			if scenario == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if _, err := os.Stat(target.Binding.Env["EF_MAIN_NOTICE_MARKER"]); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("validation started a process: %v", err)
			}
		})
	}
}

func TestMaintenanceNativeOwnerEnvelopeAndNoReplay(t *testing.T) {
	// The shared owner fixture validates separate application/untrusted context,
	// empty visible input, exact thread and inherited settings on every write.
	for _, scenario := range []string{"pm-content", "no-final", "empty-final", "active", "baseline-busy", "unsupported", "wrong-thread", "wrong-owner", "stale-receipt", "permission", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			target, fixture := newNoticeOwnerFixture(t, scenario)
			target.Binding.MainSessionID = target.SessionID
			target.Binding.Args = []string{"--dangerously-bypass-approvals-and-sandbox"}
			ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
			defer cancel()
			result, err := (MainNoticeRunner{Target: target}).RunMaintenance(ctx, noticeCodexRules, noticeCodexData)
			success := scenario == "pm-content" || scenario == "no-final" || scenario == "empty-final"
			if success {
				want := ""
				if scenario == "pm-content" {
					want = "收到测试消息"
				} else if scenario == "empty-final" {
					want = " \n "
				}
				if err != nil || result != (Result{Text: want, SessionID: noticeSession, RunID: noticeNewTurn}) {
					t.Fatalf("completion receipt lost: %+v %v", result, err)
				}
			} else if err == nil || result != (Result{}) {
				t.Fatalf("unverified completion accepted: %+v %v", result, err)
			}
			if MainNoticeCanRetry(err) != (scenario == "active" || scenario == "baseline-busy") {
				t.Fatalf("unsafe retry classification: %v", err)
			}
			starts := 0
			for _, request := range fixture.requests() {
				var method string
				_ = json.Unmarshal(request["method"], &method)
				if method == "thread-follower-start-turn" {
					starts++
				}
			}
			wantStarts := 1
			if scenario == "active" || scenario == "baseline-busy" || scenario == "unsupported" || scenario == "wrong-thread" {
				wantStarts = 0
			}
			if starts != wantStarts {
				t.Fatalf("unexpected submission or replay: %d want %d", starts, wantStarts)
			}
			if scenario == "unsupported" {
				return
			}
			readerCalls, readErr := os.ReadFile(filepath.Clean(target.Binding.Env["EF_MAIN_NOTICE_MARKER"]))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, call := range strings.Fields(string(readerCalls)) {
				switch call {
				case "initialize", "initialized", "thread/read", "thread/turns/list":
				default:
					t.Fatalf("readback took over thread or execution settings: %s", readerCalls)
				}
			}
		})
	}
}
