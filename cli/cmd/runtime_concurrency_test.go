package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
)

// Run the actual report/sync paths in separate CLI processes sharing one Home.
// The HTTP barrier guarantees an old config is held across the newer write.
func TestRuntimeConcurrentProcessesKeepNewIdentity(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		for _, staleOperation := range []string{"report", "sync"} {
			t.Run(strconv.FormatBool(v2)+"/"+staleOperation, func(t *testing.T) {
				entered, release := make(chan struct{}, 1), make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mode := r.Header.Get("X-Client-Mode")
					if (staleOperation == "report" && r.Method == http.MethodPut && mode == "plugin") ||
						(staleOperation == "sync" && r.Method == http.MethodGet && r.URL.Path != "/api/v1/probe" && r.URL.Path != "/api/v2/probe") {
						entered <- struct{}{}
						<-release
					}
					if r.URL.Path == "/api/v1/probe" || r.URL.Path == "/api/v2/probe" {
						if mode != "skill" || r.Header.Get("X-Client-Host") != "hermes/0.20.0" {
							t.Errorf("later process headers host=%q mode=%q", r.Header.Get("X-Client-Host"), mode)
						}
					}
					_, _ = w.Write([]byte(`{"code":0,"data":{"feed_poll_interval":7200}}`))
				}))
				defer server.Close()
				defer unblock()
				cfg, serverName := runtimeTestConfig(t, server.URL, v2)
				if err := cfg.SetKV(settingsSyncedKey, "1"); err != nil {
					t.Fatal(err)
				}
				if _, err := configureRuntimeIdentity(cfg, serverName, "plugin", "openclaw", "2026.9.1"); err != nil {
					t.Fatal(err)
				}
				oldProcess := startRuntimeProcess(t, "old-"+staleOperation)
				select {
				case <-entered:
				case result := <-oldProcess:
					t.Fatalf("old process finished before request barrier: %s", result)
				case <-time.After(15 * time.Second):
					t.Fatal("old process did not reach HTTP barrier")
				}
				waitRuntimeProcess(t, startRuntimeProcess(t, "new-report"))
				want := loadRuntimeTestState(t, serverName)
				if want.Host != "hermes/0.20.0" || want.Mode != "skill" || want.ReportedAtMillis == 0 {
					t.Fatalf("new identity=%+v", want)
				}
				fresh, err := config.Load()
				if err != nil {
					t.Fatal(err)
				}
				if err := fresh.SetKV("concurrent_marker", "new-write"); err != nil {
					t.Fatal(err)
				}
				unblock()
				waitRuntimeProcess(t, oldProcess)
				if got := loadRuntimeTestState(t, serverName); got != want {
					t.Fatalf("old %s overwrote newer state: got=%+v want=%+v", staleOperation, got, want)
				}
				if staleOperation == "report" {
					fresh, err = config.Load()
					if err != nil {
						t.Fatal(err)
					}
					if fresh.GetKV("concurrent_marker") != "new-write" {
						t.Fatal("report rewrote an old whole config snapshot")
					}
				}
				waitRuntimeProcess(t, startRuntimeProcess(t, "probe"))
			})
		}
	}
}

func startRuntimeProcess(t *testing.T, operation string) <-chan string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRuntimeConcurrentProcessHelper$")
	child.Env = append(os.Environ(), "EIGENFLUX_RUNTIME_TEST_OPERATION="+operation, "EIGENFLUX_HOME="+config.HomeDir())
	done := make(chan string, 1)
	go func() {
		out, err := child.CombinedOutput()
		if err != nil {
			done <- fmt.Sprintf("%v: %s", err, out)
		} else {
			done <- ""
		}
	}()
	return done
}

func waitRuntimeProcess(t *testing.T, done <-chan string) {
	t.Helper()
	select {
	case failure := <-done:
		if failure != "" {
			t.Fatal(failure)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("runtime subprocess timed out")
	}
}

func TestRuntimeConcurrentProcessHelper(t *testing.T) {
	operation := os.Getenv("EIGENFLUX_RUNTIME_TEST_OPERATION")
	if operation == "" {
		return
	}
	clientMeta, version, serverFlag = client.Meta{Host: "terminal", Channel: "cli"}, "0.0.44", ""
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	switch operation {
	case "old-report":
		result, err := reportRuntimeSettings(cfg, "", "", "", "", true)
		if err != nil || result.Status != "reported" {
			t.Fatalf("old report=%+v err=%v", result, err)
		}
	case "old-sync":
		if err := SyncSettings(cfg); err != nil {
			t.Fatal(err)
		}
	case "new-report":
		result, err := reportRuntimeSettings(cfg, "skill", "", "hermes", "0.20.0", false)
		if err != nil || result.Status != "reported" {
			t.Fatalf("new report=%+v err=%v", result, err)
		}
	case "probe":
		c, err := newSettingsClient(cfg, activeServerName())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Get("/probe", nil); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown operation %q", operation)
	}
}
