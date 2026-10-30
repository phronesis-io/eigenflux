package cmd

import (
	"bufio"
	"bytes"
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
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/selfupdate"
	"github.com/gorilla/websocket"
)

func TestWatchAutomaticallyUpdatesWithoutHeartbeatOrAgent(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("builds real CLIs and verifies Unix process handoff")
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	directory := t.TempDir()
	for _, v := range []string{"0.0.47", "0.0.48"} {
		build := exec.Command("go", "build", "-ldflags", "-X main.Version="+v+" -X cli.eigenflux.ai/internal/skills.VerifyPublicKeyBase64="+base64.StdEncoding.EncodeToString(pub), "-o", filepath.Join(directory, v), ".")
		build.Dir = ".."
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build CLI: %v %s", err, output)
		}
	}
	oldBytes, _ := os.ReadFile(filepath.Join(directory, "0.0.47"))
	newBytes, _ := os.ReadFile(filepath.Join(directory, "0.0.48"))
	sum := sha256.Sum256(newBytes)
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	release := selfupdate.Manifest{Version: "0.0.48", Artifacts: map[string]selfupdate.Artifact{name: {SHA256: hex.EncodeToString(sum[:]), Size: int64(len(newBytes))}}}
	artifactPath, artifact := heartbeatUpdateArtifactFixture(t, directory, name, newBytes, &release, key)
	if err := selfupdate.Sign(&release, key); err != nil {
		t.Fatal(err)
	}
	var manifests, packages atomic.Int32
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/agents/me/settings":
			_, _ = out.Write([]byte(`{"code":0,"data":{"auto_cli_update":true}}`))
		case "/cli/latest/release.json", "/cli/latest/package-release.json":
			manifests.Add(1)
			_ = json.NewEncoder(out).Encode(release)
		case "/cli/0.0.48/" + artifactPath:
			packages.Add(1)
			_, _ = out.Write(artifact)
		case "/api/v2/agent/events/ws":
			connection, err := upgrader.Upgrade(out, r, nil)
			if err != nil {
				return
			}
			defer connection.Close()
			for {
				if _, _, err := connection.ReadMessage(); err != nil {
					return
				}
			}
		case "/api/v2/runtime/control/stream":
			out.Header().Set("Content-Type", "text/event-stream")
			_, _ = out.Write([]byte(": connected\n\n"))
			out.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/api/v2/runtime/heartbeat":
			_, _ = out.Write([]byte(`{"code":0,"data":{"pending_command_ids":[],"next_heartbeat_seconds":30}}`))
		case "/api/v2/pm/fetch":
			_, _ = out.Write([]byte(`{"code":0,"data":{"messages":[],"next_cursor":""}}`))
		case "/api/v2/maintenance/events:batch":
			_, _ = out.Write([]byte(`{"code":0,"data":{}}`))
		default:
			t.Errorf("watch contacted an unrelated endpoint: %s", r.URL.Path)
			http.NotFound(out, r)
		}
	}))
	defer srv.Close()
	cfg, server := runtimeTestConfig(t, srv.URL, true)
	if err := cfg.UpdateServer(server, srv.URL, strings.Replace(srv.URL, "http:", "ws:", 1)); err != nil {
		t.Fatal(err)
	}
	home := config.HomeDir()
	credentials, _ := auth.LoadV2Credentials(server)
	credentials.PrincipalID = "principal-1"
	if err := auth.SaveV2Credentials(server, credentials); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(home, "servers", server, "agent-v2-credentials.json")
	before, _ := os.ReadFile(credentialPath)
	stable := filepath.Join(directory, "eigenflux")
	if err := os.WriteFile(stable, oldBytes, 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, stable, "--homedir", home, "--server", server, "watch", "--desktop-notifications=false")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "EIGENFLUX_INSTALL_") && !strings.HasPrefix(value, watchAgentEnv+"=") && !strings.HasPrefix(value, watchPrincipalEnv+"=") && !strings.HasPrefix(value, updateReexecEnv+"=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "EIGENFLUX_CDN_URL="+srv.URL, updateReexecEnv+"=0")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	events := make(chan watchEvent, 128)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var event watchEvent
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				events <- event
			}
		}
	}()
	oldStarted, newStarted := false, false
	for !newStarted {
		select {
		case event, open := <-events:
			if !open {
				_ = command.Wait()
				t.Fatalf("watch stopped before adoption: %s", stderr.String())
			}
			if event.Type != "started" {
				continue
			}
			data, _ := event.Data.(map[string]interface{})
			oldStarted = oldStarted || data["cli_version"] == "0.0.47"
			newStarted = data["cli_version"] == "0.0.48"
		case <-ctx.Done():
			t.Fatal("watch never adopted the update without a heartbeat or Agent")
		}
	}
	if !oldStarted || manifests.Load() != 1 || packages.Load() != 1 {
		t.Fatalf("incorrect autonomous update: old=%v manifest=%d package=%d", oldStarted, manifests.Load(), packages.Load())
	}
	_ = command.Process.Signal(os.Interrupt)
	if err := command.Wait(); err != nil {
		t.Fatalf("watch did not stop gracefully: %v %s", err, stderr.String())
	}
	assertHeartbeatUpdatedInstallation(t, stable, oldBytes, newBytes, pub)
	after, _ := os.ReadFile(credentialPath)
	if !bytes.Equal(before, after) {
		t.Fatal("autonomous update changed account credentials")
	}
}
