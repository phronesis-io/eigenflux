//go:build windows

package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Hold a real Windows handle without FILE_SHARE_DELETE. This exercises the OS
// refusal that follows a probe or scanner, rather than mocking os.Rename.
func holdPackageFile(t *testing.T, path string) func() {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = windows.CloseHandle(handle) }) }
	t.Cleanup(release)
	return release
}

func TestNativeWindowsPackageRenameRecoversAndPreservesOldInstallation(t *testing.T) {
	buildDir := t.TempDir()
	source := filepath.Join(buildDir, "main.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"9.8.8\")}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(buildDir, "fixture.exe")
	if out, err := exec.Command("go", "build", "-o", fixture, source).CombinedOutput(); err != nil {
		t.Fatalf("build native probe: %v %s", err, out)
	}
	binary, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	archive := testPackage(t, packageEntry{name: "eigenflux.exe", content: string(binary), mode: 0755})
	for _, scenario := range []string{"released", "persistent", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			root, pub, key := installationFixture(t)
			oldDir := writeInstallationFixture(t, root, "9.8.7", key)
			if err := ActivateInstallation(root, "9.8.7", pub); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(root + ".current.json")
			if err != nil {
				t.Fatal(err)
			}
			oldBinary, err := os.ReadFile(filepath.Join(oldDir, "eigenflux.exe"))
			if err != nil {
				t.Fatal(err)
			}
			name := "eigenflux-windows-" + runtime.GOARCH + ".exe"
			manifest := Manifest{Version: "9.8.8", Artifacts: map[string]Artifact{name: testArtifact(binary)}, Packages: map[string]Artifact{name + ".zip": testArtifact(archive)}}
			if err := Sign(&manifest, key); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/cli/latest/package-release.json" {
					_ = json.NewEncoder(w).Encode(manifest)
					return
				}
				if r.URL.Path == "/cli/9.8.8/"+name+".zip" {
					_, _ = w.Write(archive)
					return
				}
				t.Errorf("unexpected path %s", r.URL.Path)
				http.NotFound(w, r)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var installAt time.Time
			options := Options{Home: t.TempDir(), Executable: root, Version: "9.8.7", Key: pub, CDN: server.URL, CompletePackage: true}
			options.OnProgress = func(p Progress) {
				if p.Phase != "install" {
					return
				}
				files, err := filepath.Glob(filepath.Join(root+".versions", ".stage-*", "eigenflux.exe"))
				if err != nil || len(files) != 1 {
					t.Fatalf("staged executable: %v %v", files, err)
				}
				release := holdPackageFile(t, files[0])
				installAt = time.Now()
				if scenario == "released" {
					go func() { time.Sleep(350 * time.Millisecond); release() }()
				}
				if scenario == "cancelled" {
					go func() { time.Sleep(150 * time.Millisecond); cancel() }()
				}
			}
			result := Check(ctx, options)
			elapsed := time.Since(installAt)
			if installAt.IsZero() {
				t.Fatalf("did not reach installation: %+v", result)
			}
			if scenario == "released" {
				if result.Status != "updated" || elapsed < 300*time.Millisecond || elapsed > 3*time.Second {
					t.Fatalf("temporary handle was not retried: %+v elapsed=%s", result, elapsed)
				}
				active, err := ResolveInstallation(root, pub)
				if err != nil || active.Version != "9.8.8" {
					t.Fatalf("verified activation: %+v %v", active, err)
				}
			} else {
				if result.Status != "failed" || result.Phase != "install" {
					t.Fatalf("denial changed result: %+v", result)
				}
				if scenario == "persistent" && (elapsed < 1800*time.Millisecond || elapsed > 4*time.Second || result.Reason != "install_package_rename_permission_denied" && result.Reason != "install_package_rename_file_in_use") {
					t.Fatalf("retry bound/error lost: %+v elapsed=%s", result, elapsed)
				}
				if scenario == "cancelled" && (elapsed < 100*time.Millisecond || elapsed > time.Second || result.Reason != "install_package_rename_cancelled") {
					t.Fatalf("cancellation ignored: %+v elapsed=%s", result, elapsed)
				}
				after, _ := os.ReadFile(root + ".current.json")
				if string(after) != string(before) {
					t.Fatal("failed install changed active pointer")
				}
				if _, err := os.Stat(filepath.Join(root+".versions", "9.8.8")); !os.IsNotExist(err) {
					t.Fatal("failed rename created target release")
				}
			}
			retained, _ := os.ReadFile(filepath.Join(oldDir, "eigenflux.exe"))
			entry, _ := os.ReadFile(root)
			if string(retained) != string(oldBinary) || string(entry) != "stable entry" {
				t.Fatal("old installation was modified")
			}
		})
	}
}

func TestNativeWindowsPackageRenameDoesNotRetryMissingSource(t *testing.T) {
	dir := t.TempDir()
	started := time.Now()
	err := renamePackage(context.Background(), filepath.Join(dir, "missing"), filepath.Join(dir, "target"))
	if !errors.Is(err, os.ErrNotExist) || time.Since(started) > time.Second {
		t.Fatalf("non-retryable error: %v", err)
	}
}
