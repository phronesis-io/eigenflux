package selfupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
)

// Use the real CLI entry, including ForwardInstallation, so a version probe
// cannot silently inherit a root that rejects its temporary staging directory.
func TestRealCLIConsecutiveCompleteUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two actual CLI versions")
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cliDirectory, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(directory, versionExecutableName())
	home := filepath.Join(directory, "agent-home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var firstBinary []byte
	manifests := map[string][]byte{}
	payloads := map[string][]byte{}
	for _, version := range []string{"9.8.7", "9.8.8"} {
		fixture := filepath.Join(directory, "cli-"+version+filepath.Ext(versionExecutableName()))
		flags := "-X main.Version=" + version + " -X cli.eigenflux.ai/internal/skills.VerifyPublicKeyBase64=" + base64.StdEncoding.EncodeToString(public)
		build := exec.Command("go", "build", "-ldflags", flags, "-o", fixture, ".")
		build.Dir = cliDirectory
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build actual CLI %s: %v %s", version, err, output)
		}
		binary, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if firstBinary == nil {
			firstBinary = binary
		}
		entries := []packageEntry{{name: versionExecutableName(), content: string(binary), mode: 0755}}
		if runtime.GOOS == "darwin" {
			entries = append(entries, realCLITestHelper(t, directory, "9.8.7", firstBinary)...)
		}
		archive := testPackage(t, entries...)
		manifest := Manifest{Version: version, Artifacts: map[string]Artifact{name: testArtifact(binary)}, Packages: map[string]Artifact{name + ".zip": testArtifact(archive)}}
		entry := manifest
		entry.Packages = nil
		if err := Sign(&entry, key); err != nil {
			t.Fatal(err)
		}
		payloads["/cli/"+version+"/release.json"], err = json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if version == "9.8.8" {
			manifest.NotifierVersion = "9.8.7"
		}
		if err := Sign(&manifest, key); err != nil {
			t.Fatal(err)
		}
		manifests[version], err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		payloads["/cli/"+version+"/"+name+".zip"] = archive
		payloads["/cli/"+version+"/"+name] = binary
	}
	if err := os.WriteFile(root, firstBinary, 0755); err != nil {
		t.Fatal(err)
	}
	var latest atomic.Value
	latest.Store(manifests["9.8.7"])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"auto_cli_update":true}}`))
			return
		}
		if r.URL.Path == "/cli/v2/latest/release.json" {
			var selected Manifest
			_ = json.Unmarshal(latest.Load().([]byte), &selected)
			selected.Packages = nil
			if err := Sign(&selected, key); err != nil {
				t.Error(err)
				http.Error(w, "sign", 500)
				return
			}
			raw, _ := json.Marshal(selected)
			_, _ = w.Write(raw)
			return
		}
		if r.URL.Path == "/cli/latest/release.json" {
			_, _ = w.Write(payloads["/cli/9.8.7/release.json"])
			return
		}
		if r.URL.Path == "/cli/v2/latest/package-release.json" {
			var selected Manifest
			raw := latest.Load().([]byte)
			_ = json.Unmarshal(raw, &selected)
			if selected.NotifierVersion == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(raw)
			return
		}
		if r.URL.Path == "/cli/latest/package-release.json" {
			// Old clients continue receiving the signed bridge while v2 moves on.
			_, _ = w.Write(manifests["9.8.7"])
			return
		}
		if payload, ok := payloads[r.URL.Path]; ok {
			_, _ = w.Write(payload)
			return
		}
		t.Errorf("unexpected/bare artifact request: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	options := Options{Home: home, Executable: root, Version: "9.8.7", Key: public, CDN: server.URL, CompletePackage: true, CheckNow: true}
	first := Check(context.Background(), options)
	if first.Status != "updated" || first.Version != "9.8.7" {
		t.Fatalf("same-version bootstrap: %+v", first)
	}
	t.Setenv(InstallRootEnv, root)
	t.Setenv(InstallChildEnv, "1")
	t.Setenv(installWatchLauncherEnv, "1")
	latest.Store(manifests["9.8.8"])
	options.Executable = first.Executable
	second := Check(context.Background(), options)
	if second.Status != "updated" || second.Version != "9.8.8" {
		t.Fatalf("second complete update from version child: %+v", second)
	}
	// Model the already-shipped stable entry verifier exactly: unknown v2
	// fields are discarded before re-marshalling the v1 signature payload.
	oldData, err := os.ReadFile(filepath.Join(root+".versions", "9.8.8", "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Version   string              `json:"version"`
		Artifacts map[string]Artifact `json:"artifacts"`
		Packages  map[string]Artifact `json:"packages,omitempty"`
		Signature string              `json:"signature,omitempty"`
	}
	if err = json.Unmarshal(oldData, &old); err != nil {
		t.Fatal(err)
	}
	oldSignature, err := base64.StdEncoding.DecodeString(old.Signature)
	if err != nil {
		t.Fatal(err)
	}
	old.Signature = ""
	oldPayload, _ := json.Marshal(old)
	if old.Version != "9.8.8" || !ed25519.Verify(public, append([]byte(manifestDomain), oldPayload...), oldSignature) {
		t.Fatal("old stable launcher cannot authenticate the active independent-App release")
	}
	active, err := ResolveInstallation(root, public)
	if err != nil || active.Version != "9.8.8" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	// A new user invocation is not an admitted internal call from the old
	// bare watch; that call intentionally remains pinned until its handoff.
	t.Setenv(InstallRootEnv, "")
	t.Setenv(InstallChildEnv, "")
	t.Setenv(installWatchLauncherEnv, "")
	probe := exec.Command(root, "--homedir", home, "version", "--short")
	output, err := probe.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "9.8.8" {
		t.Fatalf("stable entry did not forward latest version: %v %s", err, output)
	}
	// Model an old updater resuming the bridge with a fresh daily throttle.
	// One invocation must adopt v2 without ageing that state or issuing update.
	chain := filepath.Join(directory, "automatic")
	chainHome := filepath.Join(chain, ".eigenflux")
	account := filepath.Join(chainHome, "servers", "audit")
	if err := os.MkdirAll(account, 0700); err != nil {
		t.Fatal(err)
	}
	configBody, _ := json.Marshal(map[string]any{"default_server": "audit", "servers": []map[string]string{{"name": "audit", "endpoint": server.URL, "commission_endpoint": server.URL, "stream_endpoint": server.URL}}, "kv": map[string]string{"auto_cli_update": "true", "auto_skill_sync": "false", "auto_plugin_update": "false"}})
	if err := os.WriteFile(filepath.Join(chainHome, "config.json"), configBody, 0600); err != nil {
		t.Fatal(err)
	}
	credentials := []byte(`{"agent_id":"audit-agent","principal_id":"audit-principal","access_token":"local-fixture","refresh_token":"local-fixture","expires_at":4102444800000}`)
	credentialPath := filepath.Join(account, "agent-v2-credentials.json")
	if err := os.WriteFile(credentialPath, credentials, 0600); err != nil {
		t.Fatal(err)
	}
	chainRoot := filepath.Join(chain, versionExecutableName())
	if err := os.WriteFile(chainRoot, firstBinary, 0755); err != nil {
		t.Fatal(err)
	}
	throttle, _ := json.Marshal(state{Attempt: time.Now(), PackageChecked: true, Highest: "9.8.7"})
	if err := os.WriteFile(chainRoot+".update.json", throttle, 0600); err != nil {
		t.Fatal(err)
	}
	var chainEnv []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "EIGENFLUX_") {
			chainEnv = append(chainEnv, entry)
		}
	}
	chainEnv = append(chainEnv, "EIGENFLUX_HOME="+chainHome, "EIGENFLUX_MODE=skill", "EIGENFLUX_HOST=codex", "EIGENFLUX_CDN_URL="+server.URL, "EIGENFLUX_SKIP_AGENT_SETUP=1", "EIGENFLUX_SKILLS_DIR="+filepath.Join(chain, "absent-skills"))
	runChain := exec.Command(chainRoot, "--homedir", chainHome, "--server", "audit", "heartbeat", "plan", "--format", "json")
	runChain.Env = append(append([]string(nil), chainEnv...), "EIGENFLUX_UPDATE_REEXEC=1", "EIGENFLUX_UPDATE_FROM_VERSION=1.0.0", "EIGENFLUX_UPDATE_ATTEMPT=11111111111111111111111111111111")
	chainOutput, _ := runChain.CombinedOutput() // Missing Skills may fail the resumed business plan.
	checkChain := exec.Command(chainRoot, "--homedir", chainHome, "version", "--short")
	checkChain.Env = chainEnv
	if got, err := checkChain.CombinedOutput(); err != nil || strings.TrimSpace(string(got)) != "9.8.8" {
		t.Fatalf("same-cycle continuation failed: %v %s; %s", err, got, chainOutput)
	}
	if got, err := os.ReadFile(credentialPath); err != nil || !bytes.Equal(got, credentials) {
		t.Fatal("continuation changed credentials")
	}
	retained, err := os.ReadFile(root)
	if err != nil || !bytes.Equal(retained, firstBinary) {
		t.Fatal("stable entry was replaced")
	}
}

func realCLITestHelper(t *testing.T, directory, version string, binary []byte) []packageEntry {
	t.Helper()
	app := filepath.Join(directory, "helper-"+version, "EigenFlux Notifications.app")
	macos := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(macos, "EigenFluxNotifications"), binary, 0755); err != nil {
		t.Fatal(err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>ai.eigenflux.update-test</string><key>CFBundleExecutable</key><string>EigenFluxNotifications</string><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleVersion</key><string>%s</string><key>CFBundleShortVersionString</key><string>%s</string></dict></plist>`, version, version)
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
		t.Fatalf("sign isolated test bundle: %v %s", err, output)
	}
	var entries []packageEntry
	if err := filepath.WalkDir(app, func(file string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(filepath.Dir(app), file)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, packageEntry{name: filepath.ToSlash(relative), content: string(data), mode: info.Mode()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return entries
}
