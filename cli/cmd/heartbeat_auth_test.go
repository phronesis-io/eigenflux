package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

func TestHeartbeatStopsAfterOneCredentialFailure(t *testing.T) {
	for _, reportMetadata := range []bool{false, true} {
		for _, status := range []int{401, 503, 0} {
			name := http.StatusText(status)
			if status == 0 {
				name = "network EOF"
			}
			t.Run(name+map[bool]string{true: "/metadata", false: "/access"}[reportMetadata], func(t *testing.T) {
				var challenges, business atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/api/v2/agent-sessions/refresh-challenges" {
						business.Add(1)
						w.WriteHeader(500)
						return
					}
					challenges.Add(1)
					if status == 0 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
						}
						return
					}
					code := "REFRESH_INVALID"
					if status == 503 {
						code = "AGENT_AUTH_UNAVAILABLE"
					}
					writeV2RecoveryError(w, status, code)
				}))
				defer server.Close()
				_, serverName := runtimeTestConfig(t, server.URL, true)
				if reportMetadata {
					clientMeta = client.Meta{Host: "codex", Mode: "skill"}
				}
				installHeartbeatTestRules(t)
				if _, _, _, err := auth.LoadOrCreateIdentity(serverName); err != nil {
					t.Fatal(err)
				}
				credentials, err := auth.LoadV2Credentials(serverName)
				if err != nil {
					t.Fatal(err)
				}
				credentials.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
				if err := auth.SaveV2Credentials(serverName, credentials); err != nil {
					t.Fatal(err)
				}
				err = heartbeatPlanCmd.RunE(&cobra.Command{}, nil)
				if !isV2CredentialError(err) || challenges.Load() != 1 || business.Load() != 0 {
					t.Fatalf("credential acquisition repeated or swallowed: err=%v challenges=%d business=%d", err, challenges.Load(), business.Load())
				}
				if status != 0 {
					var apiErr *client.APIError
					if !errors.As(err, &apiErr) || apiErr.StatusCode != status {
						t.Fatalf("lost original failure class: %v", err)
					}
				}
			})
		}
	}
}

func TestHeartbeatCompatibilityAuthenticationFailureStopsPlan(t *testing.T) {
	for _, status := range []int{401, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			challenges, reports := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/agent-context":
					_, _ = w.Write([]byte(`{"data":{"context_revision":1}}`))
				case "/api/v2/agent-settings/heartbeat-compatibility":
					reports++
					writeV2RecoveryError(w, status, "REPORT_FAILED")
				case "/api/v2/agent-sessions/refresh-challenges":
					challenges++
					writeV2RecoveryError(w, 401, "REFRESH_INVALID")
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			runtimeTestConfig(t, server.URL, true)
			version = "0.6.6"
			dir := installHeartbeatTestRules(t)
			manifest, err := skills.ReadLocalManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			previousKey := skills.VerifyPublicKeyBase64
			skills.VerifyPublicKeyBase64 = base64.StdEncoding.EncodeToString(publicKey)
			t.Cleanup(func() { skills.VerifyPublicKeyBase64 = previousKey })
			digest := sha256.Sum256(nil)
			manifest.TarSHA256, manifest.Sequence = hex.EncodeToString(digest[:]), 1
			if err := skills.SignManifest(manifest, privateKey); err != nil {
				t.Fatal(err)
			}
			if err := skills.WriteManifestAtomic(dir, manifest); err != nil {
				t.Fatal(err)
			}
			cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if filepath.Base(r.URL.Path) != skills.RemoteManifest {
					t.Errorf("unexpected CDN request: %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(manifest)
			}))
			defer cdn.Close()
			t.Setenv("EIGENFLUX_CDN_URL", cdn.URL)
			out, err := captureHeartbeatStdout(t, func() error { return heartbeatPlanCmd.RunE(&cobra.Command{}, nil) })
			if status == 401 {
				if !isV2CredentialError(err) || out != "" || challenges != 1 || reports != 1 {
					t.Fatalf("authentication failure became a successful plan: err=%v output=%s challenges=%d reports=%d", err, out, challenges, reports)
				}
			} else if err != nil || reports != 1 || challenges != 0 || out == "" {
				t.Fatalf("ordinary report failure blocked the plan: err=%v output=%s", err, out)
			}
		})
	}
}

func TestHeartbeatInvalidCredentialFileRequiresLocalIdentityRecovery(t *testing.T) {
	_, serverName := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	installHeartbeatTestRules(t)
	path := filepath.Join(config.HomeDir(), "servers", serverName, "agent-v2-credentials.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	err := heartbeatPlanCmd.RunE(&cobra.Command{}, nil)
	if !isV2CredentialError(err) || !isV2IdentityUnavailable(err) || !v2CredentialNeedsLogin(err) {
		t.Fatalf("unusable credentials bypassed owner recovery: %v", err)
	}
}

func TestHeartbeatMetadataServiceFailureDoesNotBlockAuthenticatedPlan(t *testing.T) {
	settings, context := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/agents/me/settings" {
			settings++
			writeV2RecoveryError(w, 503, "SETTINGS_UNAVAILABLE")
			return
		}
		if r.URL.Path == "/api/v2/agent-context" {
			context++
			_, _ = w.Write([]byte(`{"data":{"context_revision":1}}`))
			return
		}
		t.Errorf("unexpected request: %s", r.URL.Path)
	}))
	defer server.Close()
	runtimeTestConfig(t, server.URL, true)
	clientMeta = client.Meta{Host: "codex", Mode: "skill"}
	installHeartbeatTestRules(t)
	_, err := captureHeartbeatStdout(t, func() error { return heartbeatPlanCmd.RunE(&cobra.Command{}, nil) })
	if err != nil || settings != 1 || context != 1 {
		t.Fatalf("metadata service failure blocked the plan: err=%v settings=%d context=%d", err, settings, context)
	}
}
