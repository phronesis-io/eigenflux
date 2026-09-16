package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/config"
	watchstate "cli.eigenflux.ai/internal/watch"
)

func TestUninstallInstallationRegistry(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "eigenflux")
	first := installationHome{Home: filepath.Join(dir, "one"), Host: "codex", SkillsTarget: filepath.Join(dir, "skills")}
	if err := registerInstallation(executable, first); err != nil {
		t.Fatal(err)
	}
	first.SkillsTarget = filepath.Join(dir, "other-skills")
	if err := registerInstallation(executable, first); err != nil {
		t.Fatal(err)
	}
	if err := registerInstallation(executable, installationHome{Home: filepath.Join(dir, "two"), Host: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	var record installationRecord
	data, _ := os.ReadFile(executable + ".install.json")
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Homes) != 2 || record.Homes[0].SkillsTarget != first.SkillsTarget {
		t.Fatalf("incorrect registry: %+v", record)
	}
}

func TestUninstallLocksRemovedServer(t *testing.T) {
	home := t.TempDir()
	release, err := watchstate.Acquire(home, "removed-server")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if releases, err := acquireUninstallWatchLocks(home, nil); err == nil {
		for _, release := range releases {
			release()
		}
		t.Fatal("active removed-server watch was ignored")
	}
}

func TestUninstallLocksNewConfiguredServer(t *testing.T) {
	home := t.TempDir()
	releases, err := acquireUninstallWatchLocks(home, []config.Server{{Name: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	if release, err := watchstate.Acquire(home, "new"); err == nil {
		release()
		t.Fatal("new watch can start during uninstall")
	}
}

func TestUninstallIsolatedExecutable(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles an isolated CLI")
	}
	buildDir := filepath.Join(t.TempDir(), "build")
	if err := os.MkdirAll(buildDir, 0700); err != nil {
		t.Fatal(err)
	}
	name := "eigenflux"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	built := filepath.Join(buildDir, name)
	build := exec.Command("go", "build", "-o", built, ".")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build isolated CLI: %v\n%s", err, out)
	}
	binary, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	fixture := func(t *testing.T) (string, string, func(...string) ([]byte, error)) {
		dir := t.TempDir()
		bin := filepath.Join(dir, name)
		home := filepath.Join(dir, "agent", ".eigenflux")
		if err := os.WriteFile(bin, binary, 0755); err != nil {
			t.Fatal(err)
		}
		bin, err = filepath.EvalSymlinks(bin)
		if err != nil {
			t.Fatal(err)
		}
		invoke := func(args ...string) ([]byte, error) {
			cmd := exec.Command(bin, append([]string{"--homedir", home, "--format", "json"}, args...)...)
			cmd.Env = append(os.Environ(), "HOME="+dir, "USERPROFILE="+dir, "EIGENFLUX_HOME="+home, "EIGENFLUX_SKILLS_DIR="+filepath.Join(dir, "skills"))
			return cmd.CombinedOutput()
		}
		if out, err := invoke("installation", "record", "--host", "codex", "--skills-target", ""); err != nil {
			t.Fatalf("record: %v %s", err, out)
		}
		return bin, home, invoke
	}
	t.Run("preview_and_apply_preserve_home", func(t *testing.T) {
		bin, home, invoke := fixture(t)
		credential := filepath.Join(home, "credentials.keep")
		_ = os.WriteFile(credential, []byte("fixture-only"), 0600)
		for _, suffix := range []string{".previous", ".update.json"} {
			_ = os.WriteFile(bin+suffix, []byte("fixture"), 0600)
		}
		before := snapshotUninstallTree(t, filepath.Dir(bin))
		out, err := invoke("uninstall", "--reason", "preview only")
		if err != nil {
			t.Fatalf("preview: %v %s", err, out)
		}
		after := snapshotUninstallTree(t, filepath.Dir(bin))
		if !bytes.Equal(before, after) {
			t.Fatal("preview mutated fixture files")
		}
		if out, err = invoke("uninstall", "--apply"); err == nil || !strings.Contains(string(out), "native tools") {
			t.Fatalf("missing host cleanup accepted: %v %s", err, out)
		}
		if out, err = invoke("uninstall", "--apply", "--host-cleanup-confirmed", "--reason", "fixture retirement"); err != nil {
			t.Fatalf("apply: %v %s", err, out)
		}
		var result struct {
			Pending bool `json:"executable_removal_pending"`
		}
		_ = json.Unmarshal(out, &result)
		if runtime.GOOS == "windows" {
			if !result.Pending {
				t.Fatal("Windows self-removal not pending")
			}
		} else {
			for _, path := range []string{bin, bin + ".previous", bin + ".update.json", bin + ".install.json"} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("left installation file: %s %v", path, err)
				}
			}
		}
		if data, _ := os.ReadFile(credential); string(data) != "fixture-only" {
			t.Fatal("Agent data removed")
		}
		reason, _ := os.ReadFile(filepath.Join(home, "uninstall.json"))
		if !strings.Contains(string(reason), "fixture retirement") {
			t.Fatal("reason not preserved locally")
		}
	})
	t.Run("shared_homes_require_review", func(t *testing.T) {
		bin, home, invoke := fixture(t)
		other := filepath.Join(filepath.Dir(bin), "second", ".eigenflux")
		if err := os.MkdirAll(other, 0700); err != nil {
			t.Fatal(err)
		}
		if err := registerInstallation(bin, installationHome{Home: other, Host: "claude-code"}); err != nil {
			t.Fatal(err)
		}
		if out, err := invoke("uninstall", "--apply", "--host-cleanup-confirmed"); err == nil || !strings.Contains(string(out), "shared installation") {
			t.Fatalf("shared guard: %v %s", err, out)
		}
		if out, err := invoke("uninstall", "--apply", "--all-homes", "--host-cleanup-confirmed"); err != nil {
			t.Fatalf("shared apply: %v %s", err, out)
		}
		for _, path := range []string{home, other} {
			if _, err := os.Stat(path); err != nil {
				t.Fatal("shared Home removed", err)
			}
		}
	})
	t.Run("active_watch_and_update_block_removal", func(t *testing.T) {
		bin, home, invoke := fixture(t)
		release, err := watchstate.Acquire(home, "deleted-server")
		if err != nil {
			t.Fatal(err)
		}
		out, err := invoke("uninstall", "--apply", "--host-cleanup-confirmed")
		release()
		if err == nil || !strings.Contains(string(out), "active watch") {
			t.Fatalf("watch guard: %v %s", err, out)
		}
		release, err = watchstate.AcquirePath(bin + ".update.lock")
		if err != nil {
			t.Fatal(err)
		}
		out, err = invoke("uninstall", "--apply", "--host-cleanup-confirmed")
		release()
		if err == nil || !strings.Contains(string(out), "update is active") {
			t.Fatalf("update guard: %v %s", err, out)
		}
		if _, err := os.Stat(bin); err != nil {
			t.Fatal("blocked uninstall removed executable")
		}
	})
	t.Run("foreign_backup_is_preserved", func(t *testing.T) {
		bin, _, invoke := fixture(t)
		outside := filepath.Join(filepath.Dir(bin), "unrelated")
		_ = os.WriteFile(outside, []byte("keep"), 0600)
		if err := os.Symlink(outside, bin+".previous"); err != nil {
			t.Skip(err)
		}
		if out, err := invoke("uninstall", "--apply", "--host-cleanup-confirmed"); err == nil || !strings.Contains(string(out), "regular file") {
			t.Fatalf("foreign sidecar: %v %s", err, out)
		}
		if data, _ := os.ReadFile(outside); string(data) != "keep" {
			t.Fatal("foreign data changed")
		}
	})
}

func snapshotUninstallTree(t *testing.T, root string) []byte {
	t.Helper()
	snapshot := map[string]string{}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		snapshot[rel] = info.Mode().String() + "|" + info.ModTime().String()
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot[rel] += "|" + string(b)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(snapshot)
	return b
}

func TestUninstallInstallerRecordsActualExistingBinaryAndSkills(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer fixture")
	}
	source, err := os.ReadFile("../../static/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.Split(string(source), "# ── Main ─")[0]
	if prefix == string(source) {
		t.Fatal("installer main marker missing")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "other-bin")
	home := filepath.Join(dir, "agent", ".eigenflux")
	skillsDir := filepath.Join(dir, "actual-skills")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	fake := `#!/bin/sh
case "$1 $2" in
 'version --short') printf '%s\n' '1.0.0' ;;
 'skills path') printf '%s\n' "$FIXTURE_SKILLS" ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "eigenflux"), []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte("#!/bin/sh\nprintf '%s\\n' '1.0.0'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	script := prefix + "\ninstall_cli\nmigrate_config\ninstall_skills\nprintf 'RESULT:%s|%s|%s\\n' \"$INSTALLED_CLI_PATH\" \"$EF_HOME\" \"$INSTALL_SKILLS_TARGET\"\n"
	command := exec.Command("sh", "-c", script)
	command.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+dir, "EIGENFLUX_HOME="+home, "INVOKING_HOST=codex", "EIGENFLUX_HOST=codex", "FIXTURE_SKILLS="+skillsDir, "EIGENFLUX_INSTALL_DIR=", "EIGENFLUX_REF=")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("installer fixture: %v %s", err, out)
	}
	if !strings.Contains(string(out), "RESULT:"+filepath.Join(binDir, "eigenflux")+"|"+home+"|"+skillsDir) {
		t.Fatalf("incorrect installer registry inputs: %s", out)
	}
}
