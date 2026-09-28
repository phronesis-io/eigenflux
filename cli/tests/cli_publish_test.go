package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIPublisherDoesNotAdvancePointerAfterFailedUpload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash publisher runs in Linux release workflow")
	}
	root := t.TempDir()
	for _, dir := range []string{"cli/scripts", "build/cli", "bin"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile("../scripts/publish.sh")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"cli/scripts/publish.sh":          body,
		"cli/.cli.config":                 []byte("CLI_VERSION=0.0.48\n"),
		"build/cli/eigenflux-linux-amd64": []byte("fixture"),
		"build/cli/release.json":          []byte("{}"),
		"build/cli/version.txt":           []byte("0.0.48\n"),
		"bin/aws":                         []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_UPLOAD_LOG\"\nexit 1\n"),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(root, name), b, 0700); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(root, "uploads.log")
	c := exec.Command("bash", filepath.Join(root, "cli/scripts/publish.sh"))
	c.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "TEST_UPLOAD_LOG="+log, "R2_ACCESS_KEY_ID=test", "R2_SECRET_ACCESS_KEY=test", "R2_ENDPOINT=https://invalid.test", "R2_BUCKET=test", "EIGENFLUX_PUBLISH_SKILLS_WITH_CLI=false")
	if out, err := c.CombinedOutput(); err == nil {
		t.Fatalf("failed upload reported success: %s", out)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "release.json") || strings.Contains(string(b), "version.txt") || strings.Count(string(b), "\n") != 1 {
		t.Fatalf("publisher advanced after failure: %s", b)
	}
}

func TestCLIPublisherImmutableRetries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Bash")
	}
	for _, mode := range []string{"new", "same", "different", "partial-conflict", "network", "forbidden", "race-same", "race-different", "upload-failure"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"cli/scripts", "build/cli", "bin"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			body, err := os.ReadFile("../scripts/publish.sh")
			if err != nil {
				t.Fatal(err)
			}
			stub := `#!/bin/bash
printf '%s\n' "$*" >> "$TEST_UPLOAD_LOG"
op=$2
shift 2
while [[ $# -gt 0 ]]; do
  case "$1" in
    --key) key=$2; shift 2 ;;
    --bucket|--endpoint-url|--body|--if-none-match) shift 2 ;;
    *) dest=$1; shift ;;
  esac
done
if [[ "$op" == get-object ]]; then
  case "$TEST_MODE" in
    network) echo 'connection timed out' >&2; exit 1 ;;
    forbidden) echo 'An error occurred (AccessDenied)' >&2; exit 1 ;;
    different) echo different > "$dest"; exit 0 ;;
    partial-conflict)
      if [[ "$key" == */release.json ]]; then echo different > "$dest"; exit 0; fi ;;
    same) cp "$TEST_BUILD/${key##*/}" "$dest"; exit 0 ;;
    race-same|race-different)
      if [[ -f "$TEST_BUILD/raced" ]]; then
        cp "$TEST_BUILD/${key##*/}" "$dest"
        if [[ "$TEST_MODE" == race-different ]]; then echo different > "$dest"; fi
        exit 0
      fi ;;
  esac
  echo 'An error occurred (NoSuchKey) when calling the GetObject operation: missing' >&2
  exit 1
fi
if [[ "$op" == put-object ]]; then
  case "$TEST_MODE" in
    race-same|race-different) touch "$TEST_BUILD/raced"; exit 1 ;;
    upload-failure) exit 1 ;;
  esac
fi
exit 0
`
			files := map[string][]byte{
				"cli/scripts/publish.sh":          body,
				"cli/.cli.config":                 []byte("CLI_VERSION=0.0.48\n"),
				"build/cli/eigenflux-linux-amd64": []byte("fixture"),
				"build/cli/version.txt":           []byte("0.0.48\n"),
				"build/cli/release.json":          []byte("{}"),
				"bin/aws":                         []byte(stub),
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(root, name), data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			log := filepath.Join(root, "uploads.log")
			c := exec.Command("bash", filepath.Join(root, "cli/scripts/publish.sh"))
			c.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "TEST_MODE="+mode, "TEST_BUILD="+filepath.Join(root, "build/cli"), "TEST_UPLOAD_LOG="+log, "R2_ACCESS_KEY_ID=test", "R2_SECRET_ACCESS_KEY=test", "R2_ENDPOINT=https://invalid.test", "R2_BUCKET=test", "R2_PUBLIC_URL=https://invalid.test", "EIGENFLUX_PUBLISH_SKILLS_WITH_CLI=false")
			out, err := c.CombinedOutput()
			wantOK := mode == "new" || mode == "same" || mode == "race-same"
			if (err == nil) != wantOK {
				t.Fatalf("success=%v expected=%v: %s", err == nil, wantOK, out)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if !wantOK && strings.Contains(string(calls), "s3 cp") {
				t.Fatalf("advanced latest on failure: %s", calls)
			}
			if mode == "same" && strings.Contains(string(calls), "put-object") {
				t.Fatalf("rewrote existing version: %s", calls)
			}
			if mode == "partial-conflict" && strings.Contains(string(calls), "put-object") {
				t.Fatalf("wrote before complete preflight: %s", calls)
			}
			for _, line := range strings.Split(string(calls), "\n") {
				if strings.Contains(line, "put-object") && !strings.Contains(line, "--if-none-match *") {
					t.Fatalf("unconditional version upload: %s", line)
				}
			}
		})
	}
}

func TestSkillsPublicationRequiresAvailableCLI(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	script := read("../scripts/release-skills.sh")
	gate := strings.Index(script, "./cmd/clirelease")
	reserve := strings.Index(script, "skills-release-state.py\" reserve")
	if gate < 0 || reserve < gate || !strings.Contains(script, `--min-version "${SKILLS_MIN_CLI_VERSION:-$CLI_VERSION}"`) {
		t.Fatal("Skills must verify the published CLI minimum before reserving or publishing")
	}
	cliWorkflow := read("../../.github/workflows/release-cli.yml")
	skillsWorkflow := read("../../.github/workflows/release-skills.yml")
	for _, workflow := range []string{cliWorkflow, skillsWorkflow} {
		if !strings.Contains(workflow, "group: release-r2") || !strings.Contains(workflow, "cancel-in-progress: false") {
			t.Fatal("publishers must share serialization")
		}
	}
	for _, required := range []string{"workflow_run:", "workflows: [Release CLI]", "types: [completed]", "github.event.workflow_run.conclusion == 'success'"} {
		if !strings.Contains(skillsWorkflow, required) {
			t.Fatalf("missing successful CLI handoff: %s", required)
		}
	}
	publish := read("../scripts/publish.sh")
	if strings.Index(publish, `bash "$SCRIPT_DIR/release-skills.sh"`) < strings.Index(publish, `"s3://$R2_BUCKET/cli/latest/release.json"`) {
		t.Fatal("Skills dispatched before CLI pointer publication")
	}
}
