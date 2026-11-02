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
	}
	if err := os.WriteFile(root, firstBinary, 0755); err != nil {
		t.Fatal(err)
	}
	var latest atomic.Value
	latest.Store(manifests["9.8.7"])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
