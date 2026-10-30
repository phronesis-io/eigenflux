package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestShellInstallerRequiresMacOSCompletePackage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture")
	}
	source, err := os.ReadFile("../../static/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	prefix, _, found := strings.Cut(string(source), "# ── Main ─")
	if !found {
		t.Fatal("main marker missing")
	}
	for _, tc := range []struct {
		name, os                string
		existing, fail, badHash bool
	}{
		{"fresh", "Darwin", false, false, false}, {"same-version-bare", "Darwin", true, false, false},
		{"package-failure", "Darwin", false, true, false}, {"bad-bootstrap-checksum", "Darwin", false, false, true},
		{"linux", "Linux", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "tools")
			install := filepath.Join(dir, "install")
			log := filepath.Join(dir, "calls")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			cli := `#!/bin/sh
printf '%s\n' "$*" >> "$FIXTURE_LOG"
case "$*" in
 'version --short') echo 1.0.0 ;;
 *'installation replace'*)
  while [ "$1" != '--destination' ]; do shift; done
  cp "$0" "$2"; chmod +x "$2" ;;
 *' update') [ "$FIXTURE_FAIL" != 1 ] ;;
 *) : ;;
esac
`
			fixture := filepath.Join(dir, "bootstrap")
			if err := os.WriteFile(fixture, []byte(cli), 0700); err != nil {
				t.Fatal(err)
			}
			scripts := map[string]string{
				"uname": "#!/bin/sh\n[ \"$1\" = -s ] && echo \"$FIXTURE_OS\" || echo arm64\n",
				"curl": `#!/bin/sh
case "$*" in
 *version.txt*) echo 1.0.0 ;;
 *.sha256*)
  if [ "$FIXTURE_BAD_HASH" = 1 ]; then printf '%064d\n' 0; else shasum -a 256 "$FIXTURE_CLI" | awk '{print $1}'; fi ;;
 *) while [ "$1" != '-o' ]; do shift; done; cp "$FIXTURE_CLI" "$2" ;;
esac
`,
			}
			if tc.existing {
				scripts["eigenflux"] = cli
			}
			for name, body := range scripts {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("sh", "-c", prefix+"\ninstall_cli\n")
			flag := func(v bool) string {
				if v {
					return "1"
				}
				return "0"
			}
			command.Env = append(os.Environ(), "HOME="+dir, "PATH="+bin+":"+os.Getenv("PATH"), "EIGENFLUX_INSTALL_DIR="+install, "FIXTURE_CLI="+fixture, "FIXTURE_LOG="+log, "FIXTURE_OS="+tc.os, "FIXTURE_FAIL="+flag(tc.fail), "FIXTURE_BAD_HASH="+flag(tc.badHash), "EIGENFLUX_HOST=codex", "EIGENFLUX_HOME=", "SHELL=/bin/sh")
			out, err := command.CombinedOutput()
			if (err != nil) != (tc.fail || tc.badHash) {
				t.Fatalf("unexpected result %v: %s", err, out)
			}
			calls, _ := os.ReadFile(log)
			updated := strings.Contains(string(calls), "--homedir "+dir+"/.eigenflux-codex/.eigenflux update")
			if updated != (tc.os == "Darwin" && !tc.badHash) {
				t.Fatalf("wrong package/identity invocation: %s", calls)
			}
			if (tc.fail || tc.badHash) && strings.Contains(string(out), "installed successfully") {
				t.Fatalf("failure reported success: %s", out)
			}
			if tc.badHash && strings.Contains(string(calls), "installation replace") {
				t.Fatal("bad checksum executed bootstrap")
			}
		})
	}
}
