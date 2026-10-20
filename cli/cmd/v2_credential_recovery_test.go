package cmd

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

func setupV2CredentialRecovery(t *testing.T, endpoint string, createKey bool) (string, *auth.V2Credentials) {
	t.Helper()
	tempHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	server, err := cfg.GetActive("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateServer(server.Name, endpoint, ""); err != nil {
		t.Fatal(err)
	}
	if createKey {
		if _, _, _, err := auth.LoadOrCreateIdentity(server.Name); err != nil {
			t.Fatal(err)
		}
	}
	previous := &auth.V2Credentials{
		AgentID: "existing-agent", PrincipalID: "existing-principal", AccessToken: "old-access",
		RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute).UnixMilli(), Scopes: []string{"feed:read"},
	}
	if err := auth.SaveV2Credentials(server.Name, previous); err != nil {
		t.Fatal(err)
	}
	return server.Name, previous
}

func writeV2RecoveryData(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
}

func writeV2RecoveryError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"code": code, "message": "authentication unavailable"}})
}

func recoveredV2Response() map[string]interface{} {
	return map[string]interface{}{
		"agent_id": "existing-agent", "principal_id": "existing-principal", "created": false,
		"access_token": "recovered-access", "refresh_token": "recovered-refresh",
		"expires_at": time.Now().Add(15 * time.Minute).UnixMilli(), "scopes": []string{"feed:read"},
	}
}

func TestExpiredV2RefreshRecoversSameAccountAndRetriesRequest(t *testing.T) {
	for _, expiresAtStage := range []string{"challenge", "refresh"} {
		t.Run(expiresAtStage, func(t *testing.T) {
			var registrations, provisions, businessCalls atomic.Int32
			var publicKey ed25519.PublicKey
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/settings":
					businessCalls.Add(1)
					if r.Header.Get("Authorization") == "Bearer old-access" {
						writeV2RecoveryError(w, 401, "AGENT_AUTH_INVALID")
					} else if r.Header.Get("Authorization") == "Bearer recovered-access" {
						writeV2RecoveryData(w, map[string]string{"agent_id": "existing-agent"})
					} else {
						t.Errorf("unexpected authorization")
						w.WriteHeader(401)
					}
				case "/api/v2/agent-sessions/refresh-challenges":
					if expiresAtStage == "challenge" {
						writeV2RecoveryError(w, 401, "REFRESH_EXPIRED")
					} else {
						writeV2RecoveryData(w, map[string]interface{}{"nonce": "refresh-nonce", "issued_at": time.Now().UnixMilli()})
					}
				case "/api/v2/agent-sessions/refresh":
					writeV2RecoveryError(w, 401, "REFRESH_EXPIRED")
				case "/api/v2/agent-identities/recovery-challenges":
					registrations.Add(1)
					var request map[string]string
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request["public_key"] != base64.RawURLEncoding.EncodeToString(publicKey) || request["expected_agent_id"] != "existing-agent" {
						t.Error("recovery changed installation key")
					}
					writeV2RecoveryData(w, map[string]string{"bootstrap_grant": "recovery-grant", "nonce": "registration-nonce"})
				case "/api/v2/agent-identities/provision":
					provisions.Add(1)
					var request provisionV2Request
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request.ExpectedAgentID != "existing-agent" || len(request.Draft) != 0 || request.Ref != "" {
						t.Error("recovery must preserve identity without profile or referral writes")
					}
					transcript, err := provisionV2Transcript(request)
					signature, decodeErr := base64.RawURLEncoding.DecodeString(request.Signature)
					if err != nil || decodeErr != nil || !ed25519.Verify(publicKey, transcript, signature) {
						t.Error("invalid same-key recovery proof")
					}
					writeV2RecoveryData(w, recoveredV2Response())
				default:
					t.Errorf("unexpected recovery path %q", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			name, previous := setupV2CredentialRecovery(t, server.URL, true)
			publicKey, _, _ = auth.LoadIdentity(name)
			previous.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
			if err := auth.SaveV2Credentials(name, previous); err != nil {
				t.Fatal(err)
			}
			v2, _, err := newV2ClientForServer(name, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := v2.Get("/settings", nil); err != nil {
				t.Fatal(err)
			}
			saved, err := auth.LoadV2Credentials(name)
			if err != nil {
				t.Fatal(err)
			}
			if saved.AgentID != previous.AgentID || saved.PrincipalID != previous.PrincipalID || saved.AccessToken != "recovered-access" || saved.RefreshToken != "recovered-refresh" {
				t.Fatal("recovery did not persist the same account")
			}
			if registrations.Load() != 1 || provisions.Load() != 1 || businessCalls.Load() != 2 {
				t.Fatalf("calls registration=%d provision=%d business=%d", registrations.Load(), provisions.Load(), businessCalls.Load())
			}
		})
	}
}

func TestV2RefreshDoesNotRecoverOtherAuthenticationFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
	}{
		{"invalid", 401, "REFRESH_INVALID"}, {"revoked", 401, "AGENT_AUTH_INVALID"}, {"reuse", 401, "REFRESH_REUSE_DETECTED"},
		{"forbidden", 403, "AGENT_SCOPE_REQUIRED"}, {"database", 503, "AGENT_AUTH_UNAVAILABLE"},
		{"expiry-code-without-401", 503, "REFRESH_EXPIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/v2/agent-sessions/refresh-challenges" {
					t.Errorf("unexpected recovery request %s", r.URL.Path)
				}
				writeV2RecoveryError(w, tc.status, tc.code)
			}))
			defer server.Close()
			name, previous := setupV2CredentialRecovery(t, server.URL, true)
			_, err := ensureV2Credentials(name, server.URL)
			var apiErr *client.APIError
			if !isV2CredentialError(err) || !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status || apiErr.ErrorCode != tc.code {
				t.Fatalf("original API error lost: %v", err)
			}
			if v2CredentialNeedsLogin(err) != (tc.status == 401) {
				t.Fatalf("wrong login classification for %v", err)
			}
			saved, loadErr := auth.LoadV2Credentials(name)
			if loadErr != nil || !reflect.DeepEqual(saved, previous) || calls.Load() != 1 {
				t.Fatalf("failure changed credentials or retried: %v calls=%d", loadErr, calls.Load())
			}
		})
	}
}

func TestV2RecoveryRejectsIdentityAndIncompleteResponsesWithoutSaving(t *testing.T) {
	for _, change := range []string{"agent_id", "principal_id", "created", "missing_created", "refresh_token", "expires_at"} {
		t.Run(change, func(t *testing.T) {
			var provisions atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/agent-sessions/refresh-challenges":
					writeV2RecoveryError(w, 401, "REFRESH_EXPIRED")
				case "/api/v2/agent-identities/recovery-challenges":
					writeV2RecoveryData(w, map[string]string{"bootstrap_grant": "grant", "nonce": "nonce"})
				case "/api/v2/agent-identities/provision":
					provisions.Add(1)
					response := recoveredV2Response()
					switch change {
					case "agent_id", "principal_id":
						response[change] = "other-identity"
					case "created":
						response[change] = true
					case "missing_created":
						delete(response, "created")
					case "refresh_token":
						response[change] = ""
					case "expires_at":
						response[change] = time.Now().Add(-time.Minute).UnixMilli()
					}
					writeV2RecoveryData(w, response)
				default:
					t.Errorf("unexpected request %q", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			name, previous := setupV2CredentialRecovery(t, server.URL, true)
			if _, err := ensureV2Credentials(name, server.URL); err == nil {
				t.Fatal("unsafe recovery response accepted")
			}
			saved, err := auth.LoadV2Credentials(name)
			if err != nil || !reflect.DeepEqual(saved, previous) || provisions.Load() != 1 {
				t.Fatalf("unsafe response replaced local credentials: %v", err)
			}
		})
	}
}

func TestV2RefreshMissingIdentityDoesNotCreateKey(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	name, _ := setupV2CredentialRecovery(t, server.URL, false)
	_, err := ensureV2Credentials(name, server.URL)
	if !isV2CredentialError(err) || !v2CredentialNeedsLogin(err) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(config.HomeDir(), "servers", name, "identity.json")); !os.IsNotExist(err) {
		t.Fatalf("missing key was created: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("refresh sent a request without an existing key")
	}
}

func TestExistingAgentProvisionRequiresNotificationForUnavailableIdentity(t *testing.T) {
	for _, kind := range []string{"missing", "invalid", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(500)
			}))
			defer server.Close()
			name, _ := setupV2CredentialRecovery(t, server.URL, false)
			dir := filepath.Join(config.HomeDir(), "servers", name)
			identityPath := filepath.Join(dir, "identity.json")
			credentialPath := filepath.Join(dir, "agent-v2-credentials.json")
			before, err := os.ReadFile(credentialPath)
			if err != nil {
				t.Fatal(err)
			}
			invalid := []byte(`{"key_type":"ed25519-v1","private_key":"invalid"}`)
			switch kind {
			case "invalid":
				err = os.WriteFile(identityPath, invalid, 0600)
			case "symlink":
				target := filepath.Join(t.TempDir(), "original-identity.json")
				if err = os.WriteFile(target, invalid, 0600); err == nil {
					err = os.Symlink(target, identityPath)
					if err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			command := &cobra.Command{}
			command.Flags().Bool("require-existing-agent", true, "")
			command.Flags().Bool("no-handoff", true, "")
			err = agentV2ProvisionCmd.RunE(command, nil)
			if !isV2CredentialError(err) || !isV2IdentityUnavailable(err) || (kind == "missing" && !errors.Is(err, os.ErrNotExist)) {
				t.Fatalf("same-account provision lost original identity failure: %v", err)
			}
			var stderr bytes.Buffer
			if exit := reportCommandFailure(&stderr, err); exit != output.ExitAuthRequired ||
				!strings.Contains(stderr.String(), `"schema_version":"eigenflux_auth_required.v1"`) ||
				!strings.Contains(stderr.String(), `"notification_required":true`) {
				t.Fatalf("unavailable identity did not require notification: exit=%d stderr=%s", exit, &stderr)
			}
			if calls.Load() != 0 {
				t.Fatal("same-account provision sent a request without its original identity")
			}
			after, readErr := os.ReadFile(credentialPath)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("identity failure changed saved credentials: %v", readErr)
			}
			identity, statErr := os.Lstat(identityPath)
			if kind == "missing" {
				if !os.IsNotExist(statErr) {
					t.Fatalf("same-account provision created an identity: %v", statErr)
				}
			} else {
				if statErr != nil || (kind == "symlink" && identity.Mode()&os.ModeSymlink == 0) {
					t.Fatalf("identity path was replaced: %v", statErr)
				}
				data, readErr := os.ReadFile(identityPath)
				if readErr != nil || !bytes.Equal(data, invalid) {
					t.Fatalf("identity file was modified: %v", readErr)
				}
			}
		})
	}
}

func TestConcurrentExpiredV2RefreshRecoversOnlyOnce(t *testing.T) {
	var challengeCalls, provisionCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/agent-sessions/refresh-challenges":
			challengeCalls.Add(1)
			writeV2RecoveryError(w, 401, "REFRESH_EXPIRED")
		case "/api/v2/agent-identities/recovery-challenges":
			writeV2RecoveryData(w, map[string]string{"bootstrap_grant": "grant", "nonce": "nonce"})
		case "/api/v2/agent-identities/provision":
			provisionCalls.Add(1)
			writeV2RecoveryData(w, recoveredV2Response())
		default:
			t.Errorf("unexpected request %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	name, _ := setupV2CredentialRecovery(t, server.URL, true)
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			credentials, err := ensureV2Credentials(name, server.URL)
			if err != nil || credentials.AccessToken != "recovered-access" {
				t.Errorf("concurrent refresh failed: %v", err)
			}
		}()
	}
	workers.Wait()
	if challengeCalls.Load() != 1 || provisionCalls.Load() != 1 {
		t.Fatalf("duplicate recovery: challenges=%d provisions=%d", challengeCalls.Load(), provisionCalls.Load())
	}
}

func TestUnauthorizedV2RefreshReusesCredentialsRotatedByAnotherProcess(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	name, previous := setupV2CredentialRecovery(t, server.URL, true)
	previous.AccessToken = "new-access-from-other-process"
	previous.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
	if err := auth.SaveV2Credentials(name, previous); err != nil {
		t.Fatal(err)
	}
	credentials, err := refreshV2CredentialsAfterUnauthorized(name, server.URL, "old-access")
	if err != nil || credentials.AccessToken != previous.AccessToken || calls.Load() != 0 {
		t.Fatalf("already rotated credentials were refreshed again: %v calls=%d", err, calls.Load())
	}
}

func TestV2CredentialRecoveryPreservesOperationalErrors(t *testing.T) {
	for _, phase := range []string{"network", "registration", "provision"} {
		t.Run(phase, func(t *testing.T) {
			var provisions atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/agent-sessions/refresh-challenges":
					writeV2RecoveryError(w, 401, "REFRESH_EXPIRED")
				case "/api/v2/agent-identities/recovery-challenges":
					if phase == "registration" {
						writeV2RecoveryError(w, 503, "AGENT_AUTH_UNAVAILABLE")
					} else {
						writeV2RecoveryData(w, map[string]string{"bootstrap_grant": "grant", "nonce": "nonce"})
					}
				case "/api/v2/agent-identities/provision":
					provisions.Add(1)
					writeV2RecoveryError(w, 503, "AGENT_AUTH_UNAVAILABLE")
				default:
					t.Errorf("unexpected request %q", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			if phase == "network" {
				server.Close()
			}
			name, previous := setupV2CredentialRecovery(t, server.URL, true)
			_, err := ensureV2Credentials(name, server.URL)
			if !isV2CredentialError(err) || v2CredentialNeedsLogin(err) {
				t.Fatalf("operational error classified as login failure: %v", err)
			}
			if phase != "network" {
				var apiErr *client.APIError
				if !errors.As(err, &apiErr) || apiErr.StatusCode != 503 {
					t.Fatalf("503 was lost after recovery: %v", err)
				}
			}
			saved, loadErr := auth.LoadV2Credentials(name)
			if loadErr != nil || !reflect.DeepEqual(saved, previous) || provisions.Load() > 1 {
				t.Fatalf("failed recovery changed credentials or retried: %v", loadErr)
			}
		})
	}
}
