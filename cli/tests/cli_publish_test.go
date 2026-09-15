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
