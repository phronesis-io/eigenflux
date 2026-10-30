package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalInstallNotificationSetup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer fixture")
	}
	installer, err := os.ReadFile("../scripts/install-local.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, os, arch, failure  string
		wantFailure              bool
		restart, preBuildFailure bool
		args                     []string
	}{
		{name: "mac-arm64", os: "Darwin", arch: "arm64"},
		{name: "mac-intel", os: "Darwin", arch: "amd64"},
		{name: "mac-restart", os: "Darwin", arch: "arm64", restart: true},
		{name: "permission-denied-still-restarts", os: "Darwin", arch: "arm64", failure: "permission_denied", restart: true, wantFailure: true},
		{name: "setup-failed-still-restarts", os: "Darwin", arch: "arm64", failure: "helper_launch_failed", restart: true, wantFailure: true},
		{name: "restart-failed", os: "Darwin", arch: "arm64", failure: "restart", restart: true, wantFailure: true},
		{name: "helper-build-failed", os: "Darwin", arch: "arm64", failure: "build", wantFailure: true},
		{name: "permission-denied", os: "Darwin", arch: "arm64", failure: "permission_denied", wantFailure: true},
		{name: "setup-failed", os: "Darwin", arch: "arm64", failure: "helper_launch_failed", wantFailure: true},
		{name: "cli-replace-failed", os: "Darwin", arch: "arm64", failure: "cli_replace", wantFailure: true},
		{name: "helper-symlink", os: "Darwin", arch: "arm64", failure: "helper_symlink", wantFailure: true},
		{name: "linux", os: "Linux", arch: "amd64"},
		{name: "unknown-flag", os: "Darwin", arch: "arm64", args: []string{"--unknown"}, preBuildFailure: true, wantFailure: true},
		{name: "missing-target", os: "Darwin", arch: "arm64", args: []string{"--watch-launch-agent"}, preBuildFailure: true, wantFailure: true},
		{name: "missing-plist", os: "Darwin", arch: "arm64", args: []string{"--watch-launch-agent", "{plist}.missing.plist"}, preBuildFailure: true, wantFailure: true},
		{name: "relative-target", os: "Darwin", arch: "arm64", args: []string{"--watch-launch-agent", "watch.plist"}, preBuildFailure: true, wantFailure: true},
		{name: "duplicate-target", os: "Darwin", arch: "arm64", args: []string{"--watch-launch-agent", "{plist}", "--watch-launch-agent", "{plist}"}, preBuildFailure: true, wantFailure: true},
		{name: "non-mac-target", os: "Linux", arch: "amd64", restart: true, preBuildFailure: true, wantFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home, stubs, repo := filepath.Join(root, "home"), filepath.Join(root, "bin"), filepath.Join(root, "repo")
			installDir := filepath.Join(home, ".local", "bin")
			log := filepath.Join(root, "calls")
			plist := filepath.Join(home, "Library", "LaunchAgents", "ai.eigenflux.fixture with spaces.plist")
			write := func(path, text string, mode os.FileMode) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), mode); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(repo, "cli", "scripts", "install-local.sh"), string(installer), 0700)
			write(filepath.Join(repo, "cli", ".cli.config"), "CLI_VERSION=1.0.0\nEIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY=fixture\n", 0600)
			write(filepath.Join(repo, "skills", "fixture", "SKILL.md"), "fixture", 0600)
			write(filepath.Join(installDir, "eigenflux"), "old-cli", 0700)
			write(plist, `<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>ai.eigenflux.fixture</string></dict></plist>`, 0600)
			app := filepath.Join(installDir, "EigenFlux Notifications.app")
			if tc.failure == "helper_symlink" {
				external := filepath.Join(root, "external-app")
				if err := os.MkdirAll(external, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, app); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(app, "obsolete"), "old-helper", 0600)
			fakeCLI := filepath.Join(root, "fake-cli")
			write(fakeCLI, `#!/bin/bash
printf 'cli %s\n' "$*" >> "$FIXTURE_LOG"
case "$1 $2" in
  'version --short') echo 1.0.0 ;;
  'notify setup')
    test -f "$(dirname "$0")/EigenFlux Notifications.app/Contents/MacOS/helper" || exit 12
    case "$FIXTURE_FAILURE" in
      permission_denied|helper_launch_failed) echo "$FIXTURE_FAILURE" >&2; exit 13 ;;
    esac
    ;;
  'watch restart')
    [[ $# == 4 && "$3" == --launch-agent && "$4" == "$FIXTURE_PLIST" ]] || exit 16
    if [[ "$FIXTURE_FAILURE" == restart ]]; then echo 'fixture restart failure' >&2; exit 17; fi
    ;;
esac
`, 0700)
			write(filepath.Join(stubs, "go"), `#!/bin/bash
if [[ "$1 $2" == 'env GOARCH' ]]; then echo "$FIXTURE_ARCH"; exit; fi
printf 'build-cli\n' >> "$FIXTURE_LOG"
while [[ $# -gt 0 ]]; do
  if [[ "$1" == -o ]]; then cp "$FIXTURE_CLI" "$2"; exit; fi
  shift
done
exit 1
`, 0700)
			write(filepath.Join(stubs, "git"), "#!/bin/sh\necho 12345678\n", 0700)
			write(filepath.Join(stubs, "uname"), "#!/bin/sh\necho \"$FIXTURE_OS\"\n", 0700)
			write(filepath.Join(stubs, "mv"), `#!/bin/bash
if [[ "$FIXTURE_FAILURE" == cli_replace && "$*" == *'/.eigenflux-install.'*'/eigenflux '* ]]; then
  echo 'fixture CLI replacement failure' >&2
  exit 15
fi
exec /bin/mv "$@"
`, 0700)
			write(filepath.Join(repo, "cli", "scripts", "build-notifier-macos.sh"), `#!/bin/bash
printf 'build-helper %s\n' "$2" >> "$FIXTURE_LOG"
if [[ "$FIXTURE_FAILURE" == build ]]; then echo 'fixture compile failure' >&2; exit 14; fi
mkdir -p "$1/EigenFlux Notifications.app/Contents/MacOS"
printf 'new-helper' > "$1/EigenFlux Notifications.app/Contents/MacOS/helper"
`, 0700)
			args := []string{filepath.Join(repo, "cli", "scripts", "install-local.sh")}
			if tc.restart {
				args = append(args, "--watch-launch-agent", plist)
			}
			for _, arg := range tc.args {
				args = append(args, strings.ReplaceAll(arg, "{plist}", plist))
			}
			command := exec.Command("bash", args...)
			command.Env = []string{"HOME=" + home, "PATH=" + stubs + ":" + installDir + ":/usr/bin:/bin", "SHELL=/bin/bash", "FIXTURE_LOG=" + log, "FIXTURE_OS=" + tc.os, "FIXTURE_ARCH=" + tc.arch, "FIXTURE_FAILURE=" + tc.failure, "FIXTURE_CLI=" + fakeCLI, "FIXTURE_PLIST=" + plist}
			out, runErr := command.CombinedOutput()
			if (runErr != nil) != tc.wantFailure {
				t.Fatalf("unexpected result: %v\n%s", runErr, out)
			}
			calls, err := os.ReadFile(log)
			if err != nil && !(tc.preBuildFailure && os.IsNotExist(err)) {
				t.Fatal(err)
			}
			if tc.preBuildFailure && len(calls) != 0 {
				t.Fatalf("invalid arguments triggered build or install: %s", calls)
			}
			installed, err := os.ReadFile(filepath.Join(installDir, "eigenflux"))
			if err != nil {
				t.Fatal(err)
			}
			publicationFailed := tc.preBuildFailure || tc.failure == "build" || tc.failure == "cli_replace" || tc.failure == "helper_symlink"
			if publicationFailed {
				if string(installed) != "old-cli" || strings.Contains(string(calls), "cli ") {
					t.Fatalf("failed publication changed or ran installation: %s\n%s", installed, calls)
				}
				if _, err := os.Stat(filepath.Join(app, "obsolete")); err != nil {
					t.Fatal("old helper lost after publication failure", err)
				}
			} else {
				if string(installed) == "old-cli" || !strings.Contains(string(calls), "cli skills install --from-bundle "+filepath.Join(repo, "skills")) {
					t.Fatalf("CLI or skills installation missing: %s", calls)
				}
				if !tc.restart && !strings.Contains(string(out), "Restart any existing watch using its original Agent Home, server and arguments") {
					t.Fatalf("missing watch restart guidance: %s", out)
				}
			}
			restartCall := "cli watch restart --launch-agent " + plist
			if tc.restart && !publicationFailed {
				restartAt := strings.Index(string(calls), restartCall)
				if restartAt < 0 || restartAt <= strings.Index(string(calls), "cli notify setup") {
					t.Fatalf("requested restart missing after notification setup: %s", calls)
				}
			} else if strings.Contains(string(calls), "cli watch restart") {
				t.Fatalf("installer restarted an unspecified target or failed installation: %s", calls)
			}
			if tc.os == "Darwin" {
				if !tc.preBuildFailure && !strings.Contains(string(calls), "build-helper "+tc.arch) {
					t.Fatalf("wrong helper architecture: %s", calls)
				}
				if !publicationFailed {
					if _, err := os.Stat(filepath.Join(app, "obsolete")); !os.IsNotExist(err) {
						t.Fatal("helper was merged with the old bundle", err)
					}
					if !strings.Contains(string(calls), "cli notify setup") {
						t.Fatalf("notification permission was not requested: %s", calls)
					}
				}
			} else if strings.Contains(string(calls), "helper") || strings.Contains(string(calls), "notify") {
				t.Fatalf("non-macOS installer touched notification integration: %s", calls)
			}
			if tc.wantFailure {
				if strings.Contains(string(out), "Done!") {
					t.Fatalf("failed setup claimed success: %s", out)
				}
				if tc.failure == "restart" {
					if !strings.Contains(string(out), "fixture restart failure") || !strings.Contains(string(out), "watch restart failed") || !strings.Contains(string(out), "Retry:") {
						t.Fatalf("missing restart failure and recovery command: %s", out)
					}
				} else if !publicationFailed && (!strings.Contains(string(out), tc.failure) || !strings.Contains(string(out), "notification setup is incomplete") || !strings.Contains(string(out), "notify status")) {
					t.Fatalf("missing error and recovery command: %s", out)
				}
			}
		})
	}
}
