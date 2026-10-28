package uninstallscript

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPOSIXWrapperRetainsStableInstallationEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX execution")
	}
	directory := t.TempDir()
	wrapper := filepath.Join(directory, "uninstall.sh")
	cli := filepath.Join(directory, "eigenflux")
	if err := os.WriteFile(wrapper, POSIX, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("#!/bin/sh\nprintf '%s\\n' \"$EIGENFLUX_INSTALL_ROOT\" \"$EIGENFLUX_INSTALL_CHILD\" \"$EIGENFLUX_INSTALL_WATCH_LAUNCHER\" \"$@\"\n")
	if err := os.WriteFile(cli, data, 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", wrapper, cli, "--scope", "current")
	command.Env = append(os.Environ(), "EIGENFLUX_INSTALL_ROOT=stable-root", "EIGENFLUX_INSTALL_CHILD=1", "EIGENFLUX_INSTALL_WATCH_LAUNCHER=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper: %s %v", output, err)
	}
	expected := "stable-root\n1\n1\nuninstall\n--scope\ncurrent\n--script-protocol=1\n"
	if string(output) != expected {
		t.Fatalf("stable entry environment changed: %q", output)
	}
}

func TestWindowsReporterEnvironmentIsScopedToCopiedProcess(t *testing.T) {
	script := string(Windows)
	for _, marker := range []string{"EIGENFLUX_INSTALL_ROOT", "EIGENFLUX_INSTALL_CHILD", "EIGENFLUX_INSTALL_WATCH_LAUNCHER"} {
		if !strings.Contains(script, "'"+marker+"'") {
			t.Fatalf("missing reporter marker cleanup: %s", marker)
		}
	}
	if strings.Count(script, ") -TemporaryReporter") != 2 {
		t.Fatal("both finalization and failure reporting must use the temporary environment")
	}
	if strings.Contains(script, "--script-protocol=1', '--format', 'json')) -TemporaryReporter") {
		t.Fatal("original stable-entry call lost its installation environment")
	}
	if strings.Contains(script, "Remove-Item Env:") {
		t.Fatal("reporter cleanup must not mutate the wrapper environment")
	}
}
