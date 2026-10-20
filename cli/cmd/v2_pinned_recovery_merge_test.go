package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
)

func TestPinnedExpiredRefreshRecoveryPreservesIdentityFence(t *testing.T) {
	for _, phase := range []string{"challenge", "refresh"} {
		for _, switchAccount := range []bool{false, true} {
			name := phase + "/same-account"
			if switchAccount {
				name = phase + "/account-switched-before-save"
			}
			t.Run(name, func(t *testing.T) {
				var serverName string
				var before, switched *auth.V2Credentials
				provisions := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v2/agent-sessions/refresh-challenges":
						if phase == "challenge" {
							writeV2RecoveryError(w, http.StatusUnauthorized, "REFRESH_EXPIRED")
						} else {
							writeV2RecoveryData(w, map[string]any{"nonce": "fixture-refresh-nonce", "issued_at": 123})
						}
					case "/api/v2/agent-sessions/refresh":
						writeV2RecoveryError(w, http.StatusUnauthorized, "REFRESH_EXPIRED")
					case "/api/v2/agent-identities/recovery-challenges":
						writeV2RecoveryData(w, map[string]string{"bootstrap_grant": "fixture-recovery-grant", "nonce": "fixture-recovery-nonce"})
					case "/api/v2/agent-identities/provision":
						provisions++
						if switchAccount {
							// Simulate an out-of-lock writer while the HTTP response is
							// in flight. Recovery must not overwrite that new account.
							candidate := *before
							candidate.AgentID, candidate.PrincipalID = "different-agent", "different-principal"
							switched = &candidate
							if err := auth.SaveV2Credentials(serverName, switched); err != nil {
								t.Error(err)
								w.WriteHeader(500)
								return
							}
						}
						writeV2RecoveryData(w, recoveredV2Response())
					default:
						t.Errorf("unexpected request %s", r.URL.Path)
						w.WriteHeader(500)
					}
				}))
				defer server.Close()
				serverName, before = setupV2CredentialRecovery(t, server.URL, true)
				got, err := refreshPinnedV2Credentials(context.Background(), serverName, server.URL, false, before.AgentID, before.PrincipalID)
				saved, loadErr := auth.LoadV2Credentials(serverName)
				if loadErr != nil || provisions != 1 {
					t.Fatalf("recovery did not reach the fixture once: load=%v provisions=%d", loadErr, provisions)
				}
				if switchAccount {
					if !errors.Is(err, errWatchIdentity) || !reflect.DeepEqual(saved, switched) {
						t.Fatalf("recovery crossed the account fence: err=%v saved=%+v", err, saved)
					}
				} else if err != nil || got == nil || saved.AgentID != before.AgentID || saved.PrincipalID != before.PrincipalID || saved.AccessToken != "recovered-access" {
					t.Fatalf("same-account recovery failed: err=%v saved=%+v", err, saved)
				}
			})
		}
	}
}

func TestPinnedExpiredRefreshRecoveryCancellationPreservesCredentials(t *testing.T) {
	for _, phase := range []string{"recovery-challenge", "provision"} {
		t.Run(phase, func(t *testing.T) {
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				switch r.URL.Path {
				case "/api/v2/agent-sessions/refresh-challenges":
					writeV2RecoveryError(w, http.StatusUnauthorized, "REFRESH_EXPIRED")
					return
				case "/api/v2/agent-identities/recovery-challenges":
					if phase == "provision" {
						writeV2RecoveryData(w, map[string]string{"bootstrap_grant": "fixture-recovery-grant", "nonce": "fixture-recovery-nonce"})
						return
					}
				case "/api/v2/agent-identities/provision":
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				close(entered)
				<-r.Context().Done()
			}))
			defer server.Close()
			name, previous := setupV2CredentialRecovery(t, server.URL, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := refreshPinnedV2Credentials(ctx, name, server.URL, false, previous.AgentID, previous.PrincipalID)
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("recovery did not reach cancellation fixture")
			}
			cancel()
			if err := awaitPinnedError(t, done); !errors.Is(err, context.Canceled) {
				t.Fatalf("lost recovery cancellation: %v", err)
			}
			saved, err := auth.LoadV2Credentials(name)
			if err != nil || !reflect.DeepEqual(saved, previous) {
				t.Fatalf("cancelled recovery replaced credentials: %v", err)
			}
			if err := auth.WithV2CredentialsLock(name, 100*time.Millisecond, func() error { return nil }); err != nil {
				t.Fatalf("cancelled recovery retained credential lock: %v", err)
			}
		})
	}
}
