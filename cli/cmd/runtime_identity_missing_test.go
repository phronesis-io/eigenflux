package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/controlcontext"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

func TestFeedAndRuntimeDoNotReplaceUnavailableIdentity(t *testing.T) {
	for _, entry := range []struct {
		name  string
		cmd   *cobra.Command
		flags map[string]string
	}{
		{"feed poll", feedPollCmd, map[string]string{"limit": "20", "action": "refresh", "cursor": ""}},
		{"runtime heartbeat", runtimeV2HeartbeatCmd, nil},
		{"runtime command claim", runtimeV2CommandClaimCmd, map[string]string{"command-id": "42"}},
		{"runtime command complete", runtimeV2CommandCompleteCmd, map[string]string{"command-id": "42", "claim-token": "claim-proof", "claim-epoch": "1", "status": "completed", "result": "{}", "command-type": ""}},
	} {
		for _, expired := range []bool{false, true} {
			for _, missing := range []bool{true, false} {
				name := entry.name + map[bool]string{true: "/expired", false: "/fresh"}[expired] + map[bool]string{true: "/missing", false: "/invalid"}[missing]
				t.Run(name, func(t *testing.T) {
					var authRequests, operationRequests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						// The Feed command's best-effort funnel telemetry does not
						// acquire credentials or change the command's error.
						if strings.HasSuffix(r.URL.Path, "/agents/me/card/refresh-events") {
							_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
							return
						}
						if strings.Contains(r.URL.Path, "/agent-sessions/") || strings.Contains(r.URL.Path, "/agent-identities/") {
							authRequests.Add(1)
						} else {
							operationRequests.Add(1)
						}
						w.WriteHeader(http.StatusInternalServerError)
					}))
					defer server.Close()
					_, serverName := runtimeTestConfig(t, server.URL, true)
					credentials, err := auth.LoadV2Credentials(serverName)
					if err != nil {
						t.Fatal(err)
					}
					if expired {
						credentials.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
						if err := auth.SaveV2Credentials(serverName, credentials); err != nil {
							t.Fatal(err)
						}
					}
					if err := controlcontext.Save(serverName, controlcontext.Snapshot{
						OwnerAgentID: credentials.AgentID, Revision: 1, Context: json.RawMessage(`{"context_revision":1}`),
					}); err != nil {
						t.Fatal(err)
					}
					dir := filepath.Join(config.HomeDir(), "servers", serverName)
					credentialPath := filepath.Join(dir, "agent-v2-credentials.json")
					before, err := os.ReadFile(credentialPath)
					if err != nil {
						t.Fatal(err)
					}
					identityPath := filepath.Join(dir, "identity.json")
					invalidIdentity := []byte(`{"key_type":"ed25519-v1","private_key":"invalid"}`)
					if missing {
						err = os.Remove(identityPath)
					} else {
						err = os.WriteFile(identityPath, invalidIdentity, 0600)
					}
					if err != nil {
						t.Fatal(err)
					}
					for flag, value := range entry.flags {
						oldValue := entry.cmd.Flags().Lookup(flag).Value.String()
						if err := entry.cmd.Flags().Set(flag, value); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = entry.cmd.Flags().Set(flag, oldValue) })
					}
					err = entry.cmd.RunE(entry.cmd, nil)
					if !isV2CredentialError(err) || !errors.Is(err, ErrV2IdentityUnavailable) {
						t.Fatalf("identity failure lost its authentication classification: %v", err)
					}
					if authRequests.Load() != 0 || operationRequests.Load() != 0 {
						t.Fatalf("unavailable key reached the server: auth=%d operations=%d", authRequests.Load(), operationRequests.Load())
					}
					after, readErr := os.ReadFile(credentialPath)
					if readErr != nil || !bytes.Equal(before, after) {
						t.Fatalf("unavailable identity changed credentials: %v", readErr)
					}
					identityAfter, readErr := os.ReadFile(identityPath)
					if missing {
						if !os.IsNotExist(readErr) {
							t.Fatalf("missing original key was replaced: %v", readErr)
						}
					} else if readErr != nil || !bytes.Equal(identityAfter, invalidIdentity) {
						t.Fatalf("invalid original key was changed: %v", readErr)
					}
					var stderr bytes.Buffer
					if exit := reportCommandFailure(&stderr, err); exit != output.ExitAuthRequired || !strings.Contains(stderr.String(), `"code":"IDENTITY_UNAVAILABLE"`) {
						t.Fatalf("identity loss did not require owner notification: exit=%d stderr=%s", exit, &stderr)
					}
				})
			}
		}
	}
}
