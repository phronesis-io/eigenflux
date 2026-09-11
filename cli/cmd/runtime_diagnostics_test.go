package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestPartialRuntimeReportRemainsVisibleAfterFeedSuccess(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		t.Run(strconv.FormatBool(v2), func(t *testing.T) {
			reports, syncs := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					reports++
				} else {
					syncs++
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			}))
			defer server.Close()
			cfg, serverName := runtimeTestConfig(t, server.URL, v2)
			if _, err := configureRuntimeIdentity(cfg, serverName, "", "codex", ""); err != nil {
				t.Fatal(err)
			}
			for _, status := range []string{"reported", "unchanged"} {
				captured, err := os.CreateTemp(t.TempDir(), "stderr")
				if err != nil {
					t.Fatal(err)
				}
				oldStderr := os.Stderr
				os.Stderr = captured
				finishFeedPoll(cfg, serverName)
				os.Stderr = oldStderr
				if err := captured.Close(); err != nil {
					t.Fatal(err)
				}
				diagnostic, err := os.ReadFile(captured.Name())
				if err != nil {
					t.Fatal(err)
				}
				want := "EigenFlux runtime report: " + status + " (missing: mode)"
				if !strings.Contains(string(diagnostic), want) {
					t.Fatalf("partial report hidden: %q, want %q", diagnostic, want)
				}
			}
			if reports != 1 || syncs != 2 {
				t.Fatalf("partial identity interrupted Feed reconciliation: reports=%d syncs=%d", reports, syncs)
			}
		})
	}
}

func TestHeartbeatAgentOutputShowsPartialRuntimeIdentity(t *testing.T) {
	for _, status := range []string{"reported", "unchanged", "failed", "missing"} {
		plan := heartbeatPlan{RuntimeReport: runtimeReportResult{Status: status, Missing: []string{"mode"}}}
		got := renderHeartbeatPlanForAgent(plan)
		if !strings.Contains(got, "Runtime settings report: "+status+" (missing: mode)") {
			t.Fatalf("partial identity hidden: %s", got)
		}
	}
}
