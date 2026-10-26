package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMacTestInstallerPairsHelperAndRequiresSetup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS POSIX installer")
	}
	for _, scenario := range []string{"success", "setup_failed", "status_failed", "skills_failed", "skills_unverified", "helper_missing", "build_failed"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "user home")
			agent := filepath.Join(root, "Agent's existing $.eigenflux")
			bin := filepath.Join(root, "tools")
			fixture := filepath.Join(root, "fixture")
			log := filepath.Join(root, "calls")
			for _, p := range []string{agent, bin, fixture, filepath.Join(home, ".local", "bin")} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0755); err != nil {
					t.Fatal(err)
				}
			}
			launcher := filepath.Join(home, ".local", "bin", "eigenflux")
			write(launcher, "original CLI\n")
			write(filepath.Join(bin, "uname"), "#!/bin/bash\necho Darwin\n")
			write(filepath.Join(bin, "go"), `#!/bin/bash
if [[ "$1" == env ]]; then echo arm64; exit 0; fi
[[ "$SCENARIO" != build_failed ]] || exit 1
while [[ $# -gt 0 ]]; do
 if [[ "$1" == -out ]]; then OUT="$2"; break; fi
 shift
done
mkdir -p "$OUT"
cp -R "$FIXTURE/." "$OUT/"
`)
			write(filepath.Join(fixture, "eigenflux"), `#!/bin/bash
printf '%s|%s\n' "$*" "${EIGENFLUX_CDN_URL:-}" >> "$CALL_LOG"
case " $* " in
 *" notify setup "*) [[ "$SCENARIO" != setup_failed ]] ;;
 *" notify status "*) [[ "$SCENARIO" != status_failed ]] ;;
 *" skills sync "*)
 [[ "$SCENARIO" != skills_failed ]] || exit 1
 if [[ "$SCENARIO" == skills_unverified ]]; then echo '{"verified_manifest":false,"stale":true}'; else echo '{"verified_manifest":true,"stale":false}'; fi ;;

 *) exit 0 ;;
esac
`)
			write(filepath.Join(fixture, "test-source"), `#!/bin/bash
while [[ $# -gt 0 ]]; do
 if [[ "$1" == -ready-file ]]; then READY="$2"; break; fi
 shift
done
printf 'http://127.0.0.1:12345' > "$READY"
trap 'exit 0' TERM INT
while :; do sleep 0.05; done
`)
			if scenario != "helper_missing" {
				helper := filepath.Join(fixture, "EigenFlux Notifications.app", "Contents", "MacOS")
				if err := os.MkdirAll(helper, 0700); err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(helper, "EigenFluxNotifications"), "fixture")
			}
			repository, err := repositoryRoot()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", filepath.Join(repository, "cli", "scripts", "install-test-macos.sh"), "--homedir", agent, "--host", "workbuddy", "--server", "test-server")
			cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SCENARIO="+scenario, "FIXTURE="+fixture, "CALL_LOG="+log)
			out, runErr := cmd.CombinedOutput()
			if (runErr == nil) != (scenario == "success") {
				t.Fatalf("%v\n%s", runErr, out)
			}
			installed, _ := os.ReadFile(launcher)
			calls, _ := os.ReadFile(log)
			changed := scenario == "success" || scenario == "skills_failed" || scenario == "skills_unverified"
			if (string(installed) != "original CLI\n") != changed {
				t.Fatalf("unexpected launcher replacement: %s", out)
			}
			if scenario == "success" {
				if strings.Count(string(calls), "notify setup") != 1 || strings.Count(string(calls), "notify status") != 1 || strings.Count(string(calls), "skills sync --host workbuddy") != 1 {
					t.Fatalf("missing post-install steps: %s", calls)
				}
				for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
					if !strings.Contains(line, "--homedir "+agent) || !strings.HasSuffix(line, "|http://127.0.0.1:12345") {
						t.Fatalf("lost routing/test trust root: %s", line)
					}
				}
			} else if strings.Contains(string(out), "Complete test package installed:") {
				t.Fatalf("failed install claimed success: %s", out)
			}
			if scenario == "setup_failed" || scenario == "status_failed" {
				if strings.Contains(string(calls), "skills sync") {
					t.Fatal("permission failure replaced paired Skills")
				}
			}
		})
	}
}
