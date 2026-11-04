package installhome_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerPersistsRefBeforeImmediateOrDeferredProvision(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source")
	}
	installer := filepath.Join(filepath.Dir(filename), "..", "..", "static", "install.sh")
	tests := []struct {
		name         string
		args         []string
		draft        bool
		agentName    string
		endpoint     string
		saveFails    bool
		wantRef      string
		wantHomeFlag bool
	}{
		{name: "deferred onboarding", args: []string{"--ref", "EF-1234abcd"}, wantRef: "EF-1234abcd"},
		{name: "immediate onboarding", args: []string{"--ref=EF-abcd1234"}, draft: true, wantRef: "EF-abcd1234"},
		{name: "explicit home and agent name", args: []string{"--ref", "EF-1234abcd"}, draft: true, agentName: "Attribution Agent", wantRef: "EF-1234abcd", wantHomeFlag: true},
		{name: "custom source server", args: []string{"--ref", "EF-1234abcd"}, endpoint: "http://127.0.0.1:18080", wantRef: "EF-1234abcd"},
		{name: "saving failure blocks immediate onboarding", args: []string{"--ref", "EF-1234abcd"}, draft: true, saveFails: true, wantRef: "EF-1234abcd"},
		{name: "saving failure blocks deferred onboarding", args: []string{"--ref", "EF-1234abcd"}, saveFails: true, wantRef: "EF-1234abcd"},
		{name: "unattributed install"},
		{name: "malformed ref is ignored", args: []string{"--ref", "EF-bad\"ref"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			binDir := filepath.Join(home, "test bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(home, "cli.log")
			writeInstallerStub(t, filepath.Join(binDir, "eigenflux"), `#!/bin/sh
set -eu
printf 'home=<%s>' "${EIGENFLUX_HOME:-}" >> "$TEST_CLI_LOG"
for arg do printf ' <%s>' "$arg" >> "$TEST_CLI_LOG"; done
printf '\n' >> "$TEST_CLI_LOG"
if [ "${1:-}" = version ]; then printf '99.0.0\n'; fi
if [ "${3:-}" = agent ] && [ "${4:-}" = install-ref ] && [ "${TEST_SAVE_FAILS:-}" = 1 ]; then
  printf 'ref source does not match selected server\n' >&2
  exit 1
fi
`)
			writeInstallerStub(t, filepath.Join(binDir, "curl"), `#!/bin/sh
set -eu
for arg do url="$arg"; done
case "$url" in
  */cli/v2/latest/version.txt) printf '99.0.0\n200' ;;
  */cli/latest/version.txt) printf '99.0.0\n' ;;
  */api/v1/install/report) printf '200' ;;
  *) printf 'unexpected installer network request: %s\n' "$url" >&2; exit 1 ;;
esac
`)
			draftPath := ""
			if test.draft {
				draftPath = filepath.Join(home, "onboarding draft.json")
				if err := os.WriteFile(draftPath, []byte(`{"agent_profile":{}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			agentHome := filepath.Join(home, ".eigenflux-codex", ".eigenflux")
			args := append([]string{installer, "--host", "codex"}, test.args...)
			if test.wantHomeFlag {
				agentHome = filepath.Join(home, "explicit agent", ".eigenflux")
				args = append(args, "--homedir", agentHome)
			}
			endpoint := test.endpoint
			if endpoint == "" {
				endpoint = "https://www.eigenflux.ai"
			}
			saveFails := ""
			if test.saveFails {
				saveFails = "1"
			}
			command := exec.Command("sh", args...)
			command.Env = append(os.Environ(),
				"HOME="+home,
				"PATH="+binDir+string(os.PathListSeparator)+"/usr/bin:/bin",
				"EIGENFLUX_HOME=",
				"EIGENFLUX_HOST=",
				"EIGENFLUX_CDN_URL=https://cdn.invalid",
				"EIGENFLUX_API_URL="+endpoint,
				"EIGENFLUX_INSTALL_DIR="+binDir,
				"EIGENFLUX_SKIP_AGENT_SETUP=1",
				"EIGENFLUX_SETUP_HOSTS=",
				"EIGENFLUX_BOOTSTRAP_GRANT=",
				"EIGENFLUX_BOOTSTRAP_NONCE=",
				"EIGENFLUX_ONBOARDING_DRAFT_FILE="+draftPath,
				"EIGENFLUX_AGENT_NAME="+test.agentName,
				"TEST_CLI_LOG="+logPath,
				"TEST_SAVE_FAILS="+saveFails,
			)
			output, err := command.CombinedOutput()
			if err != nil && !test.saveFails {
				t.Fatalf("installer failed: %v\n%s", err, output)
			}
			if err == nil && test.saveFails {
				t.Fatalf("installer must fail when the ref cannot be preserved:\n%s", output)
			}
			logBody, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			log := string(logBody)
			refPosition := strings.Index(log, "<agent> <install-ref>")
			if test.wantRef == "" {
				if refPosition >= 0 {
					t.Fatalf("installer must not save an absent or malformed ref:\n%s", log)
				}
			} else {
				want := "home=<" + agentHome + "> <--homedir> <" + agentHome + "> <agent> <install-ref> <--ref> <" + test.wantRef + "> <--endpoint> <" + endpoint + ">"
				if !strings.Contains(log, want) {
					t.Fatalf("missing scoped ref handoff %q:\n%s", want, log)
				}
				if migrationPosition := strings.Index(log, "<migrate>"); migrationPosition < 0 || migrationPosition >= refPosition {
					t.Fatalf("ref must be saved after Home migration:\n%s", log)
				}
			}
			provisionPosition := strings.Index(log, "<agent> <provision>")
			if test.draft && !test.saveFails {
				want := "home=<" + agentHome + "> <--homedir> <" + agentHome + "> <agent> <provision> <--draft-file> <" + draftPath + ">"
				if test.agentName != "" {
					want += " <--agent-name> <" + test.agentName + ">"
				}
				if !strings.Contains(log, want) || provisionPosition <= refPosition {
					t.Fatalf("provision must use the saved ref's Home after saving:\n%s", log)
				}
				for _, line := range strings.Split(log, "\n") {
					if strings.Contains(line, "<agent> <provision>") && strings.Contains(line, "<--ref>") {
						t.Fatalf("provision must use server-scoped storage, not bypass it: %s", line)
					}
				}
			} else if provisionPosition >= 0 {
				t.Fatalf("installer must not provision without a draft or after a failed ref save:\n%s", log)
			}
			if test.saveFails && !strings.Contains(string(output), "Referral code could not be saved") {
				t.Fatalf("ref save failure must be visible:\n%s", output)
			}
		})
	}
}

func writeInstallerStub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
