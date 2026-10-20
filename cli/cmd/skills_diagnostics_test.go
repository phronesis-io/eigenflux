package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/skills"
)

func TestDoctorTableExplainsModifiedSkill(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills with spaces")
	d := skills.ModifiedDiagnostic(dir, "ef-onboarding", "local", "")
	text, err := captureHeartbeatStdout(t, func() error {
		printDoctorTable(doctorReport{SkillsDir: dir, Skills: []skills.LocalSkill{{Name: "ef-onboarding", DisplayVersion: "local", SHAMatch: false}}, Diagnostics: []skills.SyncDiagnostic{d}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MODIFIED", filepath.Join(dir, "ef-onboarding"), "Back up", "skills sync --into", "refresh the host's Skills context"} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor missing %q: %s", want, text)
		}
	}
}

func TestDoctorCommandReportsRetainedLocalEdits(t *testing.T) {
	if os.Getenv("EIGENFLUX_DOCTOR_DIAGNOSTIC_TEST") == "1" {
		formatFlag = "json"
		if err := doctorCmd.RunE(doctorCmd, nil); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	skill := filepath.Join(dir, "ef-profile")
	if err := os.MkdirAll(skill, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(skill, "SKILL.md")
	if err := os.WriteFile(file, []byte("---\nversion: 1.0.0\n---\nofficial"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := skills.GenerateManifest(dir, "1.0.0", "", []string{"ef-profile"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := skills.WriteManifestAtomic(dir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("---\nversion: 1.0.0-local\n---\nmodified"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestDoctorCommandReportsRetainedLocalEdits$")
	command.Env = append(os.Environ(), "EIGENFLUX_DOCTOR_DIAGNOSTIC_TEST=1", "EIGENFLUX_SKILLS_DIR="+dir, "EIGENFLUX_CDN_URL=http://127.0.0.1:1", "EIGENFLUX_HOME="+t.TempDir())
	data, err := command.Output()
	if err == nil {
		t.Fatal("doctor must signal modified content")
	}
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 {
		t.Fatalf("unexpected doctor failure: %v", err)
	}
	var report doctorReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("invalid doctor JSON: %v: %s", err, data)
	}
	if len(report.Diagnostics) != 1 || report.Diagnostics[0].Path != skill || report.Diagnostics[0].LocalVersion != "1.0.0-local" || !strings.Contains(report.Diagnostics[0].Hint, "Back up") {
		t.Fatalf("missing doctor diagnostic: %+v", report)
	}
}

func TestSkillsSyncDiagnosticsKeepJSONAndQuietContracts(t *testing.T) {
	if os.Getenv("EIGENFLUX_SYNC_DIAGNOSTIC_TEST") == "1" {
		formatFlag = "json"
		for key, value := range map[string]string{"into": os.Getenv("EIGENFLUX_SKILLS_DIR"), "if-stale": "true", "quiet": os.Getenv("EIGENFLUX_SYNC_DIAGNOSTIC_QUIET")} {
			if err := skillsSyncCmd.Flags().Set(key, value); err != nil {
				t.Fatal(err)
			}
		}
		if err := skillsSyncCmd.RunE(skillsSyncCmd, nil); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	folder := filepath.Join(dir, "ef-profile")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte("---\nversion: 1.0.0\n---\nofficial"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := skills.GenerateManifest(dir, "1.0.0", "", []string{"ef-profile"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := skills.WriteManifestAtomic(dir, manifest); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	for _, quiet := range []string{"false", "true"} {
		t.Run("quiet="+quiet, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestSkillsSyncDiagnosticsKeepJSONAndQuietContracts$")
			command.Env = append(os.Environ(), "EIGENFLUX_SYNC_DIAGNOSTIC_TEST=1", "EIGENFLUX_SYNC_DIAGNOSTIC_QUIET="+quiet, "EIGENFLUX_SKILLS_DIR="+dir, "EIGENFLUX_CDN_URL="+server.URL, "EIGENFLUX_HOME="+t.TempDir())
			var stderr bytes.Buffer
			command.Stderr = &stderr
			data, err := command.Output()
			if err != nil {
				t.Fatalf("sync failed: %v: %s", err, stderr.String())
			}
			var result skills.SyncResult
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatalf("stdout is not one JSON document: %v: %s", err, data)
			}
			if len(result.Diagnostics) != 1 || !result.NoNetwork {
				t.Fatalf("missing retention diagnostic: %+v", result)
			}
			if quiet == "true" && stderr.Len() != 0 {
				t.Fatalf("quiet sync emitted stderr: %s", stderr.String())
			}
			if quiet == "false" && !strings.Contains(stderr.String(), result.SkillsDir) {
				t.Fatalf("normal sync hid retention path: %s", stderr.String())
			}
		})
	}
}
