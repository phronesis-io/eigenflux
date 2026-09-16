package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativeLauncherQuoting(t *testing.T) {
	home := filepath.Join(t.TempDir(), "user's home")
	ps, err := renderHeartbeatLauncher("C:\\Program Files\\eigenflux.exe", home, "server's name", "skill", "powershell")
	if err != nil || !strings.Contains(ps, "$env:EIGENFLUX_MODE='skill'; & '") || !strings.Contains(ps, "server''s name") {
		t.Fatalf("%q %v", ps, err)
	}
	if _, err := renderHeartbeatLauncher("cli", home, "%PATH%", "skill", "cmd"); err == nil {
		t.Fatal("cmd expansion accepted")
	}
	if runtime.GOOS == "windows" {
		return
	}
	bin := filepath.Join(t.TempDir(), "fake cli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$EIGENFLUX_MODE\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	launcher, err := renderHeartbeatLauncher(bin, home, "server '$HOME'", "skill", "posix")
	if err != nil {
		t.Fatal(err)
	}
	result, err := exec.Command("sh", "-c", launcher).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "skill\n--homedir\n" + home + "\n--server\nserver '$HOME'\nheartbeat\nplan\n--format\nagent\n"
	if string(result) != want {
		t.Fatalf("got %q want %q", result, want)
	}
}

func TestNativeCLIPrefixUsesShellInvocation(t *testing.T) {
	home := filepath.Join(t.TempDir(), "owner's home")
	ps, err := renderHeartbeatCLIPrefix("C:\\Program Files\\eigenflux.exe", home, "owner's server", "powershell")
	if err != nil || !strings.HasPrefix(ps, "& 'C:\\Program Files\\eigenflux.exe'") || !strings.Contains(ps, "owner''s server") {
		t.Fatalf("bad PowerShell prefix %q: %v", ps, err)
	}
	if _, err := renderHeartbeatCLIPrefix("eigenflux.exe", home, "%PATH%", "cmd"); err == nil {
		t.Fatal("unsafe CMD prefix")
	}
	if runtime.GOOS == "windows" {
		return
	}
	bin := filepath.Join(t.TempDir(), "fake cli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	prefix, err := renderHeartbeatCLIPrefix(bin, home, "server '$HOME'", "posix")
	if err != nil {
		t.Fatal(err)
	}
	got, err := exec.Command("sh", "-c", prefix+" version --short").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "--homedir\n" + home + "\n--server\nserver '$HOME'\nversion\n--short\n"
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
