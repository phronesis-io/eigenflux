package selfupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func installationFixture(t *testing.T) (string, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, versionExecutableName())
	if err := os.WriteFile(root, []byte("stable entry"), 0700); err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return root, pub, key
}

func writeInstallationFixture(t *testing.T, root, version string, key ed25519.PrivateKey) string {
	t.Helper()
	dir := filepath.Join(root+".versions", version)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	bin := []byte("verified CLI " + version)
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	sum := sha256.Sum256(bin)
	manifest := Manifest{Version: version, Artifacts: map[string]Artifact{name: {SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bin))}}}
	if err := Sign(&manifest, key); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "release.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, versionExecutableName()), bin, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestInstallationActivationPreservesEntryAndRejectsDowngrade(t *testing.T) {
	root, pub, key := installationFixture(t)
	writeInstallationFixture(t, root, "1.0.2", key)
	writeInstallationFixture(t, root, "1.0.1", key)
	if err := ActivateInstallation(root, "1.0.2", pub); err != nil {
		t.Fatal(err)
	}
	active, err := ResolveInstallation(root, pub)
	if err != nil || active.Version != "1.0.2" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	if err := ActivateInstallation(root, "1.0.1", pub); err == nil {
		t.Fatal("downgrade accepted")
	}
	if err := ActivateInstallation(root, "1.0.2", pub); err != nil {
		t.Fatalf("idempotent activation: %v", err)
	}
	entry, _ := os.ReadFile(root)
	if string(entry) != "stable entry" {
		t.Fatal("running entry overwritten")
	}
}

func TestInstallationRejectsIncompleteTamperedAndUntrustedReleases(t *testing.T) {
	for _, scenario := range []string{"missing", "hash", "signature", "version", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root, pub, key := installationFixture(t)
			writeInstallationFixture(t, root, "1.0.1", key)
			if err := ActivateInstallation(root, "1.0.1", pub); err != nil {
				t.Fatal(err)
			}
			dir := writeInstallationFixture(t, root, "1.0.2", key)
			bin := filepath.Join(dir, versionExecutableName())
			switch scenario {
			case "missing":
				_ = os.Remove(bin)
			case "hash":
				_ = os.WriteFile(bin, []byte("tampered"), 0700)
			case "signature":
				_, _, other := installationFixture(t)
				writeInstallationFixture(t, root, "1.0.2", other)
			case "version":
				old, _ := os.ReadFile(filepath.Join(root+".versions", "1.0.1", "release.json"))
				_ = os.WriteFile(filepath.Join(dir, "release.json"), old, 0600)
			case "symlink":
				_ = os.Remove(bin)
				if err := os.Symlink(root, bin); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			if err := ActivateInstallation(root, "1.0.2", pub); err == nil {
				t.Fatal("invalid installation activated")
			}
			active, err := ResolveInstallation(root, pub)
			if err != nil || active.Version != "1.0.1" {
				t.Fatalf("previous release lost: %+v %v", active, err)
			}
		})
	}
}

func TestInstallationPointerRejectsPathsAndInvalidExistingPointer(t *testing.T) {
	root, pub, key := installationFixture(t)
	writeInstallationFixture(t, root, "1.0.2", key)
	for _, data := range []string{`{"version":"../../escape"}`, `{"version":"1.0.2","executable":"elsewhere"}`, `{"version":"1.0.2"} {}`, `{"version":"1.0.3"}`} {
		if err := os.WriteFile(root+".current.json", []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveInstallation(root, pub); err == nil {
			t.Fatalf("accepted invalid pointer %s", data)
		}
		if err := ActivateInstallation(root, "1.0.2", pub); err == nil {
			t.Fatalf("overwrote invalid existing pointer %s", data)
		}
	}
}

func TestInstallationConcurrentAccountsShareActivation(t *testing.T) {
	root, pub, key := installationFixture(t)
	writeInstallationFixture(t, root, "1.0.1", key)
	writeInstallationFixture(t, root, "1.0.2", key)
	var wg sync.WaitGroup
	for _, version := range []string{"1.0.1", "1.0.2", "1.0.2", "1.0.1"} {
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			_ = ActivateInstallation(root, v, pub)
		}(version)
	}
	wg.Wait()
	// A contender may have observed the lock as busy; the next normal check
	// retries activation. It must neither corrupt the pointer nor downgrade it.
	if err := ActivateInstallation(root, "1.0.2", pub); err != nil {
		t.Fatal(err)
	}
	active, err := ResolveInstallation(root, pub)
	if err != nil || active.Version != "1.0.2" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
}

func TestInstallationRootAndEnvironmentAreScoped(t *testing.T) {
	root, _, key := installationFixture(t)
	dir := writeInstallationFixture(t, root, "1.0.2", key)
	t.Setenv(InstallRootEnv, root)
	got, err := InstallationRoot(filepath.Join(dir, versionExecutableName()))
	if err != nil || got != root {
		t.Fatalf("root=%s err=%v", got, err)
	}
	other := filepath.Join(filepath.Dir(root), "other.exe")
	_ = os.WriteFile(other, []byte("other"), 0700)
	if _, err := InstallationRoot(other); err == nil {
		t.Fatal("unrelated executable accepted inherited root")
	}
	env := InstallationEnvironment([]string{"KEEP=value", InstallRootEnv + "=/stale", InstallChildEnv + "=stale", "EIGENFLUX_HOME=account-a"}, root)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "stale") || !strings.Contains(joined, "EIGENFLUX_HOME=account-a") || strings.Count(joined, InstallRootEnv+"=") != 1 {
		t.Fatalf("invalid scoped environment %q", env)
	}
}

func TestInstallationFirstBareWatchAdoptionRetainsStableRoot(t *testing.T) {
	root, _, key := installationFixture(t)
	first := writeInstallationFixture(t, root, "1.0.1", key)
	next := writeInstallationFixture(t, root, "1.0.2", key)
	t.Setenv(InstallRootEnv, "")
	got, err := AdoptionInstallationRoot(root, filepath.Join(next, versionExecutableName()))
	if err != nil || got != root {
		t.Fatalf("first adoption root=%s err=%v", got, err)
	}
	t.Setenv(InstallRootEnv, root)
	got, err = AdoptionInstallationRoot(filepath.Join(first, versionExecutableName()), filepath.Join(next, versionExecutableName()))
	if err != nil || got != root {
		t.Fatalf("later adoption root=%s err=%v", got, err)
	}
	otherRoot, _, otherKey := installationFixture(t)
	otherDir := writeInstallationFixture(t, otherRoot, "1.0.2", otherKey)
	if _, err := AdoptionInstallationRoot(root, filepath.Join(otherDir, versionExecutableName())); err == nil {
		t.Fatal("adopted an unrelated installation")
	}
}
