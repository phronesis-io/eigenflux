package cmd

import (
	"bytes"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/selfupdate"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestHeartbeatInstalledProbeSignalRollback(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("builds a real CLI and uses POSIX signals and a shell candidate")
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	buildDir := t.TempDir()
	oldPath := filepath.Join(buildDir, "old-cli")
	build := exec.Command("go", "build", "-ldflags", "-X main.Version=0.0.47 -X cli.eigenflux.ai/internal/skills.VerifyPublicKeyBase64="+base64.StdEncoding.EncodeToString(pub), "-o", oldPath, ".")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %s %v", out, err)
	}
	oldBytes, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	// Only the staged probe succeeds. The installed probe announces readiness
	// and execs sleep so cancellation kills the probe without orphan descendants.
	candidate := []byte("#!/bin/sh\ncase \"$0\" in */.eigenflux-update-*) echo 0.0.48;; *) : > \"$EF_ROLLBACK_PROBE_READY\"; exec sleep 30;; esac\n")
	sum := sha256.Sum256(candidate)
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	release := selfupdate.Manifest{Version: "0.0.48", Artifacts: map[string]selfupdate.Artifact{name: {SHA256: hex.EncodeToString(sum[:]), Size: int64(len(candidate))}}}
	if err := selfupdate.Sign(&release, key); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		signal os.Signal
	}{{"TERM", syscall.SIGTERM}, {"INT", os.Interrupt}} {
		t.Run(tc.name, func(t *testing.T) {
			var unexpected atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cli/latest/release.json":
					_ = json.NewEncoder(w).Encode(release)
				case "/cli/0.0.48/" + name:
					_, _ = w.Write(candidate)
				default:
					unexpected.Add(1)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			tempHome(t)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			server, err := cfg.GetActive("")
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.UpdateServer(server.Name, srv.URL, ""); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			bin, ready := filepath.Join(dir, "eigenflux"), filepath.Join(dir, "ready")
			if err := os.WriteFile(bin, oldBytes, 0755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, bin, "--homedir", config.HomeDir(), "--server", server.Name, "heartbeat", "plan")
			child.Env = append(os.Environ(), "EIGENFLUX_HOME="+config.HomeDir(), "EIGENFLUX_CDN_URL="+srv.URL, "EIGENFLUX_MODE=skill", "EIGENFLUX_HOST=codex", "EIGENFLUX_UPDATE_REEXEC=0", "EF_ROLLBACK_PROBE_READY="+ready)
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			defer func() { _ = child.Process.Kill() }()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("exited before installed probe: %v %s", err, stderr.String())
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("installed probe did not start")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if installed, err := os.ReadFile(bin); err != nil || !bytes.Equal(installed, candidate) {
				t.Fatalf("candidate not installed: %v", err)
			}
			if err := child.Process.Signal(tc.signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil || !strings.Contains(stderr.String(), "context canceled") {
					t.Fatalf("cycle did not exit through cancellation: %v %s", err, stderr.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled updater did not finish rollback promptly")
			}
			if restored, err := os.ReadFile(bin); err != nil || !bytes.Equal(restored, oldBytes) {
				t.Fatalf("old CLI not restored: %v", err)
			}
			if stdout.Len() != 0 || unexpected.Load() != 0 {
				t.Fatalf("cycle continued: output=%q requests=%d", stdout.String(), unexpected.Load())
			}
			if _, err := os.Stat(bin + ".previous"); !os.IsNotExist(err) {
				t.Fatalf("rollback did not consume backup: %v", err)
			}
			staged, err := filepath.Glob(filepath.Join(dir, ".eigenflux-update-*"))
			if err != nil || len(staged) != 0 {
				t.Fatalf("temporary files remain: %v %v", staged, err)
			}
		})
	}
}
