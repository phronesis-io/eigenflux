package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"github.com/spf13/cobra"
)

func TestCLIUpdateIdentityRejectsChangedAccountBeforeBusiness(t *testing.T) {
	for _, kind := range []string{"agent", "principal", "endpoint", "Home", "server"} {
		t.Run(kind, func(t *testing.T) {
			cfg, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
			serverFlag = server
			identity, err := captureCLIUpdateIdentity(server)
			if err != nil || identity == nil {
				t.Fatalf("capture identity: %v", err)
			}
			encoded, err := json.Marshal(identity)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(updateIdentityEnv, string(encoded))
			t.Setenv(updateReexecEnv, "1")
			switch kind {
			case "agent", "principal":
				credentials, err := auth.LoadV2Credentials(server)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "agent" {
					credentials.AgentID = "another-agent"
				} else {
					credentials.PrincipalID = "another-principal"
				}
				if err := auth.SaveV2Credentials(server, credentials); err != nil {
					t.Fatal(err)
				}
			case "endpoint":
				if err := cfg.UpdateServer(server, "http://127.0.0.1:2", ""); err != nil {
					t.Fatal(err)
				}
			case "Home":
				config.SetHomeDir(t.TempDir())
				t.Cleanup(func() { config.SetHomeDir("") })
			case "server":
				if err := cfg.AddServer("other", "http://127.0.0.1:2"); err != nil {
					t.Fatal(err)
				}
				serverFlag = "other"
			}
			if kind != "server" && identity.verify() == nil {
				t.Fatal("parent accepted changed identity")
			}
			// Exercise the production pre-run hook: no command RunE has run yet.
			oldHomeFlag := homeDirFlag
			homeDirFlag = ""
			t.Cleanup(func() { homeDirFlag = oldHomeFlag })
			if err := rootCmd.PersistentPreRunE(&cobra.Command{}, nil); err == nil {
				t.Fatal("replacement accepted changed identity before business")
			}
		})
	}
}

func TestCLIUpdateIdentityAllowsTokenRotationWithoutObservationReceipt(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	serverFlag = server
	identity, err := captureCLIUpdateIdentity(server)
	if err != nil {
		t.Fatal(err)
	}
	credentials, _ := auth.LoadV2Credentials(server)
	credentials.AccessToken, credentials.RefreshToken = "rotated-access", "rotated-refresh"
	if err := auth.SaveV2Credentials(server, credentials); err != nil {
		t.Fatal(err)
	}
	if err := identity.verify(); err != nil {
		t.Fatalf("parent rejected token refresh: %v", err)
	}
	encoded, _ := json.Marshal(identity)
	t.Setenv(updateIdentityEnv, string(encoded))
	t.Setenv(updateReexecEnv, "1")
	t.Setenv(updateAttemptEnv, "no-maintenance-receipt")
	if err := verifyCLIUpdateReexecIdentity(); err != nil {
		t.Fatalf("token rotation or missing observation receipt blocked handoff: %v", err)
	}
}

func TestCLIUpdateIdentityNoLoginAndStaleEnvironment(t *testing.T) {
	tempHome(t)
	identity, err := captureCLIUpdateIdentity("unused")
	if err != nil || identity != nil {
		t.Fatalf("login required for owner update: %+v %v", identity, err)
	}
	t.Setenv(updateIdentityEnv, "invalid stale identity")
	t.Setenv(updateReexecEnv, "0")
	if err := verifyCLIUpdateReexecIdentity(); err != nil {
		t.Fatal("ordinary commands consumed stale identity")
	}
	env := heartbeatReexecEnvironment(os.Environ())
	for _, value := range env {
		if strings.HasPrefix(value, updateIdentityEnv+"=") {
			t.Fatal("previous update identity leaked into next handoff")
		}
	}
	t.Setenv(updateReexecEnv, "1")
	if err := verifyCLIUpdateReexecIdentity(); err == nil {
		t.Fatal("invalid handoff identity accepted")
	}
}
