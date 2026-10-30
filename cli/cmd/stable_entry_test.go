package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/selfupdate"
)

func TestStableEntryIsUsedByPersistedCommands(t *testing.T) {
	if os.Getenv("EF_STABLE_ENTRY_FIXTURE") == "1" {
		stable, err := stableInstalledExecutable()
		if err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": err.Error()})
			os.Exit(0)
		}
		actual, err := installedExecutable()
		if err != nil {
			t.Fatal(err)
		}
		home := os.Getenv("EF_STABLE_ENTRY_HOME")
		launcher, err := nativeHeartbeatLauncher(home, "pinned-server", "skill", "posix")
		if err != nil {
			t.Fatal(err)
		}
		prefix, err := nativeHeartbeatCLIPrefix(home, "pinned-server", "posix", "skill")
		if err != nil {
			t.Fatal(err)
		}
		if err := registerInstallation(stable, installationHome{Home: home, Host: "codex"}); err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"stable": stable, "actual": actual, "launcher": launcher, "prefix": prefix}); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := "eigenflux"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	stable := filepath.Join(directory, name)
	versioned := filepath.Join(stable+".versions", "1.0.34", name)
	if err := os.MkdirAll(filepath.Dir(versioned), 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versioned, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stable, []byte("stable installation entry"), 0700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(directory, "account", ".eigenflux")
	for _, foreign := range []bool{false, true} {
		root := stable
		if foreign {
			root = filepath.Join(directory, "foreign-eigenflux")
			if err := os.WriteFile(root, []byte("foreign installation"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		command := exec.Command(versioned, "-test.run=^TestStableEntryIsUsedByPersistedCommands$")
		command.Env = append(os.Environ(), "EF_STABLE_ENTRY_FIXTURE=1", "EF_STABLE_ENTRY_HOME="+home, selfupdate.InstallRootEnv+"="+root)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture: %s %v", output, err)
		}
		result := map[string]string{}
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatalf("result %q: %v", output, err)
		}
		if foreign {
			if result["error"] == "" {
				t.Fatal("foreign installation root became a persisted entry")
			}
			continue
		}
		if result["stable"] != stable || result["actual"] != versioned {
			t.Fatalf("entry/resource distinction lost: %v", result)
		}
		for _, key := range []string{"launcher", "prefix"} {
			if !strings.Contains(result[key], shellQuote(stable)) || strings.Contains(result[key], ".versions") {
				t.Fatalf("%s pinned a release directory: %v", key, result)
			}
		}
		if _, err := os.Stat(stable + ".install.json"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(versioned + ".install.json"); !os.IsNotExist(err) {
			t.Fatal("installation registration pinned a release directory")
		}
		wrapper := stable + ".uninstall.sh"
		if runtime.GOOS == "windows" {
			wrapper = stable + ".uninstall.ps1"
		}
		if _, err := os.Stat(wrapper); err != nil {
			t.Fatalf("stable uninstall wrapper: %v", err)
		}
	}
}
