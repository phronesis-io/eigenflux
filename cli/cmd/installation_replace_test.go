package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	watchstate "cli.eigenflux.ai/internal/watch"
)

func TestReplaceInstallationHonorsUninstallLocks(t *testing.T) {
	for _, suffix := range []string{".install.lock", ".update.lock"} {
		t.Run(suffix, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source, destination := filepath.Join(dir, "download"), filepath.Join(dir, "eigenflux")
			os.WriteFile(source, []byte("new CLI"), 0755)
			os.WriteFile(destination, []byte("old CLI"), 0755)
			release, err := watchstate.AcquirePath(destination + suffix)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if err := replaceInstallation(source, destination, installationHome{Home: filepath.Join(dir, ".eigenflux")}); err == nil {
				t.Fatal("replaced CLI while uninstall lock was held")
			}
			content, _ := os.ReadFile(destination)
			if string(content) != "old CLI" {
				t.Fatalf("installed file changed: %q", content)
			}
			if _, err := os.Stat(destination + ".install.json"); !os.IsNotExist(err) {
				t.Fatalf("unexpected registration: %v", err)
			}
		})
	}
}

func TestReplaceInstallationRecordsOwner(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, destination := filepath.Join(dir, "download"), filepath.Join(dir, "eigenflux")
	if err := os.WriteFile(source, []byte("new CLI"), 0755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, ".eigenflux")
	target := filepath.Join(dir, "skills")
	if err := registerInstallation(destination, installationHome{Home: home, Host: "codex", SkillsTarget: target}); err != nil {
		t.Fatal(err)
	}
	if err := replaceInstallation(source, destination, installationHome{Home: home, Host: "codex"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(destination)
	if err != nil || string(content) != "new CLI" {
		t.Fatalf("replacement: %q, %v", content, err)
	}
	content, err = os.ReadFile(destination + ".install.json")
	if err != nil {
		t.Fatal(err)
	}
	var record installationRecord
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Homes) != 1 || record.Homes[0].Home != home || record.Homes[0].SkillsTarget != target {
		t.Fatalf("missing owner: %+v", record)
	}
}

func TestReplaceInstallationUsesCanonicalLock(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realDir, aliasDir := filepath.Join(dir, "real"), filepath.Join(dir, "alias")
	if err := os.Mkdir(realDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	source := filepath.Join(dir, "download")
	if err := os.WriteFile(source, []byte("new CLI"), 0755); err != nil {
		t.Fatal(err)
	}
	release, err := watchstate.AcquirePath(filepath.Join(realDir, "eigenflux.install.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := replaceInstallation(source, filepath.Join(aliasDir, "eigenflux"), installationHome{Home: filepath.Join(dir, ".eigenflux")}); err == nil {
		t.Fatal("symlink alias bypassed uninstall lock")
	}
	if _, err := os.Stat(filepath.Join(realDir, "eigenflux")); !os.IsNotExist(err) {
		t.Fatalf("binary installed despite lock: %v", err)
	}
}
