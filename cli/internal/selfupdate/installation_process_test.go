package selfupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeInstallationProcessFixture(t *testing.T, root, version string, key ed25519.PrivateKey) {
	t.Helper()
	dir := writeInstallationFixture(t, root, version, key)
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, versionExecutableName()), bin, 0755); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "release.json"))
	var manifest Manifest
	_ = json.Unmarshal(data, &manifest)
	sum := sha256.Sum256(bin)
	for name := range manifest.Artifacts {
		manifest.Artifacts[name] = Artifact{Size: int64(len(bin)), SHA256: hex.EncodeToString(sum[:])}
	}
	if err := Sign(&manifest, key); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "release.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationLoopChild(t *testing.T) {
	if os.Getenv("TEST_INSTALLATION_LOOP_CHILD") != "1" {
		return
	}
	path, _ := os.Executable()
	version := filepath.Base(filepath.Dir(path))
	f, err := os.OpenFile(os.Getenv("TEST_INSTALLATION_RECEIPT"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(91)
	}
	fmt.Fprintf(f, "%s %d\n", version, os.Getppid())
	_ = f.Close()
	if version == "1.0.1" {
		if os.Getenv("TEST_INSTALLATION_ADVANCE") == "1" {
			data, _ := json.Marshal(installationPointer{Version: "1.0.2"})
			if err := os.WriteFile(os.Getenv(InstallRootEnv)+".current.json", data, 0600); err != nil {
				os.Exit(92)
			}
		}
		os.Exit(InstallRestartExitCode)
	}
	os.Exit(0)
}

func TestInstallationWatchLoopAdoptsWithoutParentChain(t *testing.T) {
	for _, scenario := range []string{"adopt", "unchanged", "ordinary-exit"} {
		t.Run(scenario, func(t *testing.T) {
			root, pub, key := installationFixture(t)
			writeInstallationProcessFixture(t, root, "1.0.1", key)
			writeInstallationProcessFixture(t, root, "1.0.2", key)
			if err := ActivateInstallation(root, "1.0.1", pub); err != nil {
				t.Fatal(err)
			}
			receipt := filepath.Join(filepath.Dir(root), "launches")
			env := append(os.Environ(), "TEST_INSTALLATION_LOOP_CHILD=1", "TEST_INSTALLATION_RECEIPT="+receipt,
				"EIGENFLUX_HOME="+filepath.Dir(root), "EIGENFLUX_WATCH_EXPECTED_AGENT=agent-a", "EIGENFLUX_WATCH_EXPECTED_PRINCIPAL=principal-a")
			if scenario != "unchanged" {
				env = append(env, "TEST_INSTALLATION_ADVANCE=1")
			}
			env = InstallationEnvironment(env, root)
			installation, err := ResolveInstallation(root, pub)
			if err != nil {
				t.Fatal(err)
			}
			err = runInstallationChildren(installation, pub, []string{"-test.run=^TestInstallationLoopChild$"}, env, scenario != "ordinary-exit")
			data, _ := os.ReadFile(receipt)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			switch scenario {
			case "adopt":
				if err != nil || len(lines) != 2 || lines[0] != "1.0.1 "+strconv.Itoa(os.Getpid()) || lines[1] != "1.0.2 "+strconv.Itoa(os.Getpid()) {
					t.Fatalf("launches=%q err=%v", data, err)
				}
			case "unchanged":
				if err == nil || !strings.Contains(err.Error(), "no newer active release") || len(lines) != 1 {
					t.Fatalf("launches=%q err=%v", data, err)
				}
			case "ordinary-exit":
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != InstallRestartExitCode || len(lines) != 1 {
					t.Fatalf("ordinary command relaunched: launches=%q err=%v", data, err)
				}
			}
		})
	}
}
