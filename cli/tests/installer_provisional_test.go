package tests

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerProvisionalSkillsRemainOutsideAutomaticRemoval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer fixture")
	}
	source, err := os.ReadFile("../../static/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	prefix, _, found := strings.Cut(string(source), "# ── Main ─")
	if !found {
		t.Fatal("installer main marker missing")
	}
	for _, priorManifest := range []bool{false, true} {
		name := "fresh"
		if priorManifest {
			name = "previously-managed"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			binDir, target := filepath.Join(dir, "bin"), filepath.Join(dir, "skills")
			for _, path := range []string{binDir, target} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if priorManifest {
				if err := os.WriteFile(filepath.Join(target, ".ef-manifest.json"), []byte(`{"managed_by":"eigenflux-cli"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			archive := filepath.Join(dir, "skills.tar.gz")
			f, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			for _, skill := range []string{"ef-broadcast", "ef-uninstall", "ef-localdev"} {
				body := "fixture " + skill
				if err := tw.WriteHeader(&tar.Header{Name: "eigenflux-main/skills/" + skill + "/SKILL.md", Mode: 0600, Size: int64(len(body))}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write([]byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			for _, closer := range []interface{ Close() error }{tw, gz, f} {
				if err := closer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			stubs := map[string]string{
				"eigenflux": "#!/bin/sh\ncase \"$1 $2\" in\n 'skills path') printf '%s\\n' \"$FIXTURE_TARGET\" ;;\n *) exit 1 ;;\nesac\n",
				"curl":      "#!/bin/sh\ncat \"$FIXTURE_ARCHIVE\"\n",
			}
			for name, body := range stubs {
				if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("sh", "-c", prefix+"\ninstall_skills\nprintf 'REGISTERED:%s\\n' \"$INSTALL_SKILLS_TARGET\"\n")
			command.Env = append(os.Environ(), "HOME="+dir, "PATH="+binDir+":"+os.Getenv("PATH"), "INVOKING_HOST=codex", "EIGENFLUX_HOST=codex", "INSTALLED_CLI_PATH="+filepath.Join(binDir, "eigenflux"), "FIXTURE_TARGET="+target, "FIXTURE_ARCHIVE="+archive)
			out, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("bootstrap failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "REGISTERED:\n") || !strings.Contains(string(out), "Uninstall will preserve provisional Skills at "+target) {
				t.Fatalf("bootstrap registered an unmanaged target or omitted its retained path: %s", out)
			}
			if _, err := os.Stat(filepath.Join(target, "ef-uninstall", "SKILL.md")); err != nil {
				t.Fatalf("uninstall skill missing: %v", err)
			}
			if _, err := os.Stat(filepath.Join(target, "ef-localdev")); !os.IsNotExist(err) {
				t.Fatalf("development skill escaped allowlist: %v", err)
			}
		})
	}
}
