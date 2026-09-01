package installhome_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerAgentHomePrecedenceAndHostIsolation(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source")
	}
	installer := filepath.Join(filepath.Dir(filename), "..", "..", "static", "install.sh")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".openclaw"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, flag, explicitEnv, host, invokingEnv, want string
	}{
		{name: "homedir flag wins", flag: "/flag/identity", explicitEnv: "/env/identity", host: "codex", want: "/flag/identity"},
		{name: "environment wins over host", explicitEnv: "/env/identity", host: "openclaw", want: "/env/identity"},
		{name: "codex ignores coexisting openclaw", host: "codex", want: filepath.Join(home, ".eigenflux-codex", ".eigenflux")},
		{name: "INVOKING_HOST environment selects codex", invokingEnv: "codex", want: filepath.Join(home, ".eigenflux-codex", ".eigenflux")},
		{name: "openclaw uses openclaw home", host: "openclaw", want: filepath.Join(home, ".openclaw", ".eigenflux")},
		{name: "claude uses default home", host: "claude-code", want: filepath.Join(home, ".eigenflux")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("sh", "-c", `. "$INSTALL_SCRIPT"; resolved_host="$TEST_HOST"; if [ -n "$TEST_INVOKING_ENV" ]; then resolved_host="$INVOKING_HOST"; fi; resolve_eigenflux_home "$TEST_FLAG" "$TEST_ENV_HOME" "$resolved_host"`)
			command.Env = append(os.Environ(),
				"HOME="+home,
				"INSTALL_SCRIPT="+installer,
				"EIGENFLUX_INSTALLER_TEST_MODE=1",
				"EIGENFLUX_HOME=",
				"EIGENFLUX_HOST=",
				"CLAUDECODE=",
				"CODEX_THREAD_ID=",
				"CODEX_SANDBOX=",
				"INVOKING_HOST="+test.invokingEnv,
				"TEST_FLAG="+test.flag,
				"TEST_ENV_HOME="+test.explicitEnv,
				"TEST_HOST="+test.host,
				"TEST_INVOKING_ENV="+test.invokingEnv,
			)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("installer resolver failed: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != test.want {
				t.Fatalf("home = %q, want %q", got, test.want)
			}
		})
	}
}
