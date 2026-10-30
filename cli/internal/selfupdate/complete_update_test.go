package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCompleteUpdateAdoptsSharedInstallationWithoutDownloading(t *testing.T) {
	root, pub, key := installationFixture(t)
	dir := writeInstallationFixture(t, root, "1.0.2", key)
	if err := ActivateInstallation(root, "1.0.2", pub); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r := Check(context.Background(), Options{Home: t.TempDir(), Executable: root, Version: "1.0.1", Key: pub, CompletePackage: true})
		if r.Status != "updated" || r.Executable != filepath.Join(dir, versionExecutableName()) || r.Attempted {
			t.Fatalf("shared installation not adopted: %+v", r)
		}
	}
	t.Setenv(InstallRootEnv, root)
	now := time.Now()
	if err := saveState(root, state{Attempt: now, Highest: "1.0.2"}); err != nil {
		t.Fatal(err)
	}
	r := Check(context.Background(), Options{Home: t.TempDir(), Executable: filepath.Join(dir, versionExecutableName()), Version: "1.0.2", Key: pub, Now: now})
	if r.Status != "deferred" {
		t.Fatalf("version child lost the shared throttle: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, versionExecutableName()) + ".update.json"); !os.IsNotExist(err) {
		t.Fatal("version child created nested update state")
	}
}

func TestCompleteUpdateNeverFallsBackToBareBinary(t *testing.T) {
	for _, scenario := range []string{"missing package", "checksum", "bad signature"} {
		t.Run(scenario, func(t *testing.T) {
			root, _, _ := installationFixture(t)
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			m := Manifest{Version: "1.0.2", Artifacts: map[string]Artifact{name: testArtifact([]byte("CLI"))}}
			if scenario != "missing package" {
				m.Packages = map[string]Artifact{name + ".zip": testArtifact([]byte("expected"))}
			}
			_ = Sign(&m, key)
			if scenario == "bad signature" {
				m.Version = "1.0.3"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cli/latest/package-release.json":
					_ = json.NewEncoder(w).Encode(m)
				case "/cli/1.0.2/" + name + ".zip":
					_, _ = w.Write([]byte("tampered"))
				default:
					t.Errorf("complete updater requested unrelated/bare path: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			r := Check(context.Background(), Options{Home: t.TempDir(), Executable: root, Version: "1.0.1", Key: pub, CDN: server.URL, CompletePackage: true})
			if r.Status != "failed" || r.Error == "" {
				t.Fatalf("invalid package accepted: %+v", r)
			}
			if _, err := os.Stat(root + ".current.json"); !os.IsNotExist(err) {
				t.Fatal("failed update activated an installation")
			}
			data, _ := os.ReadFile(root)
			if string(data) != "stable entry" {
				t.Fatal("failed package update changed the running entry")
			}
		})
	}
}

func TestCompleteUpdateBootstrapBypassesOldThrottleOnlyOnce(t *testing.T) {
	root, pub, key := installationFixture(t)
	m := Manifest{Version: "1.0.2"}
	if err := Sign(&m, key); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/cli/latest/package-release.json" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(m)
	}))
	defer server.Close()
	now := time.Now()
	if err := saveState(root, state{Attempt: now, Highest: "1.0.2"}); err != nil {
		t.Fatal(err)
	}
	o := Options{Home: t.TempDir(), Executable: root, Version: "1.0.2", Key: pub, CDN: server.URL, Now: now, CompletePackage: true}
	first := Check(context.Background(), o)
	if first.Status != "failed" || !first.Attempted {
		t.Fatalf("same-version bootstrap skipped package validation: %+v", first)
	}
	second := Check(context.Background(), o)
	if second.Status != "deferred" || requests != 1 {
		t.Fatalf("failed bootstrap bypassed throttle repeatedly: %+v requests=%d", second, requests)
	}
}
