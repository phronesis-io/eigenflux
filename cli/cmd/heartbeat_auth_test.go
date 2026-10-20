package cmd

import (
	"bytes"
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
	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

func TestHeartbeatRecoveryRateLimitReportsBlockedCycle(t *testing.T) {
	for _, reportMetadata := range []bool{false, true} {
		t.Run(map[bool]string{false: "access", true: "metadata"}[reportMetadata], func(t *testing.T) {
			var refreshes, recoveries, otherRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/agent-sessions/refresh-challenges":
					refreshes.Add(1)
					writeV2RecoveryError(w, http.StatusUnauthorized, "REFRESH_EXPIRED")
				case "/api/v2/agent-identities/recovery-challenges":
					recoveries.Add(1)
					writeV2RecoveryError(w, http.StatusTooManyRequests, "RECOVERY_RATE_LIMITED")
				default:
					otherRequests.Add(1)
					t.Errorf("rate-limited recovery reached %s", r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			defer server.Close()
			_, serverName := runtimeTestConfig(t, server.URL, true)
			version = "0.6.7"
			t.Setenv("EIGENFLUX_HOST", "terminal")
			t.Setenv("EIGENFLUX_MODE", "")
			t.Setenv("EIGENFLUX_MODEL", "")
			if reportMetadata {
				t.Setenv("EIGENFLUX_HOST", "codex")
				t.Setenv("EIGENFLUX_MODE", "skill")
			}
			serveHeartbeatTestManifest(t, installHeartbeatTestRules(t))
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
			path := filepath.Join(config.HomeDir(), "servers", serverName, "agent-v2-credentials.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := runArgs(t, "heartbeat", "plan")
			var stderr bytes.Buffer
			if exit := reportCommandFailure(&stderr, err); exit != output.ExitUsageError {
				t.Fatalf("rate limit became a login failure: exit=%d stderr=%s", exit, &stderr)
			}
			lines := bytes.Split(bytes.TrimSpace(stderr.Bytes()), []byte("\n"))
			var notice authUnavailableNotice
			if decodeErr := json.Unmarshal(lines[len(lines)-1], &notice); decodeErr != nil ||
				notice.SchemaVersion != "eigenflux_auth_unavailable.v1" || notice.Code != "RECOVERY_RATE_LIMITED" ||
				!notice.ChecksBlocked || notice.LoginRequired || !notice.Retryable ||
				notice.Home != config.HomeDir() || notice.Server != serverName {
				t.Fatalf("host cannot distinguish rate-limited credentials from an ordinary plan failure: error=%v stderr=%s", decodeErr, &stderr)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(before, after) || stdout != "" || refreshes.Load() != 1 || recoveries.Load() != 1 || otherRequests.Load() != 0 {
				t.Fatalf("rate limit repeated authentication or changed credentials: error=%v refresh=%d recovery=%d other=%d stdout=%q", readErr, refreshes.Load(), recoveries.Load(), otherRequests.Load(), stdout)
			}
		})
	}
}

func TestHeartbeatStopsAfterOneCredentialFailure(t *testing.T) {
	for _, reportMetadata := range []bool{false, true} {
		for _, status := range []int{401, 503, 0} {
			name := http.StatusText(status)
			if status == 0 {
				name = "network EOF"
			}
			t.Run(name+map[bool]string{true: "/metadata", false: "/access"}[reportMetadata], func(t *testing.T) {
				var challenges, business atomic.Int32
				var cdnRequests *atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/api/v2/agent-sessions/refresh-challenges" {
						business.Add(1)
						w.WriteHeader(500)
						return
					}
					challenges.Add(1)
					if cdnRequests.Load() != 1 {
						t.Error("authentication started before the public Skills sync")
					}
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
				version = "0.6.6"
				t.Setenv("EIGENFLUX_HOST", "terminal")
				t.Setenv("EIGENFLUX_MODE", "")
				t.Setenv("EIGENFLUX_MODEL", "")
				if reportMetadata {
					t.Setenv("EIGENFLUX_HOST", "codex")
					t.Setenv("EIGENFLUX_MODE", "skill")
				}
				cdnRequests = serveHeartbeatTestManifest(t, installHeartbeatTestRules(t))
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
				out, err := runArgs(t, "heartbeat", "plan")
				if !isV2CredentialError(err) || challenges.Load() != 1 || business.Load() != 0 || cdnRequests.Load() != 1 || out != "" {
					t.Fatalf("Skills sync skipped, credential acquisition repeated, or failure swallowed: err=%v CDN=%d challenges=%d business=%d output=%q", err, cdnRequests.Load(), challenges.Load(), business.Load(), out)
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

func serveHeartbeatTestManifest(t *testing.T, dir string) *atomic.Int32 {
	t.Helper()
	// These authentication cases verify public synchronization before the
	// credential boundary; the shared runtime fixture disables it by default.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetKV(autoSkillSyncKey, "true"); err != nil {
		t.Fatal(err)
	}
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
	requests := new(atomic.Int32)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/skills/latest/"+skills.RemoteManifest {
			t.Errorf("unexpected CDN request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	t.Cleanup(cdn.Close)
	t.Setenv("EIGENFLUX_CDN_URL", cdn.URL)
	return requests
}

func TestHeartbeatCompatibilityAuthenticationFailureStopsPlan(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
	}{
		{"unauthorized", "REPORT_FAILED", 401},
		{"auth service unavailable", "AGENT_AUTH_UNAVAILABLE", 503},
		{"ordinary report unavailable", "REPORT_FAILED", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			challenges, reports := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/agent-context":
					_, _ = w.Write([]byte(`{"data":{"context_revision":1}}`))
				case "/api/v2/agent-settings/heartbeat-compatibility":
					reports++
					writeV2RecoveryError(w, tc.status, tc.code)
				case "/api/v2/agent-sessions/refresh-challenges":
					challenges++
					writeV2RecoveryError(w, 401, "REFRESH_INVALID")
				case "/api/v2/maintenance/events:batch":
					writeV2RecoveryData(w, map[string]any{})
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			runtimeTestConfig(t, server.URL, true)
			version = "0.6.6"
			serveHeartbeatTestManifest(t, installHeartbeatTestRules(t))
			out, err := captureHeartbeatStdout(t, func() error { return heartbeatPlanCmd.RunE(&cobra.Command{}, nil) })
			if tc.status == 401 {
				if !isV2CredentialError(err) || out != "" || challenges != 1 || reports != 1 {
					t.Fatalf("authentication failure became a successful plan: err=%v output=%s challenges=%d reports=%d", err, out, challenges, reports)
				}
			} else if tc.code == "AGENT_AUTH_UNAVAILABLE" {
				if !isAgentAuthUnavailable(err) || out != "" || challenges != 0 || reports != 1 {
					t.Fatalf("authentication service failure became a successful plan or retried: err=%v output=%s challenges=%d reports=%d", err, out, challenges, reports)
				}
			} else if err != nil || reports != 1 || challenges != 0 || out == "" {
				t.Fatalf("ordinary report failure blocked the plan: err=%v output=%s", err, out)
			}
		})
	}
}

func TestHeartbeatMetadataAuthenticationServiceFailureStopsPlan(t *testing.T) {
	var metadata, otherRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/api/v2/agents/me/settings" {
			metadata.Add(1)
			writeV2RecoveryError(w, http.StatusServiceUnavailable, "AGENT_AUTH_UNAVAILABLE")
			return
		}
		otherRequests.Add(1)
		t.Errorf("authentication service failure did not stop the plan: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	runtimeTestConfig(t, server.URL, true)
	version = "0.6.6"
	t.Setenv("EIGENFLUX_HOST", "codex")
	t.Setenv("EIGENFLUX_MODE", "skill")
	cdnRequests := serveHeartbeatTestManifest(t, installHeartbeatTestRules(t))
	out, err := captureHeartbeatStdout(t, func() error {
		_, runErr := runArgs(t, "heartbeat", "plan")
		return runErr
	})
	if !isAgentAuthUnavailable(err) || metadata.Load() != 1 || otherRequests.Load() != 0 || cdnRequests.Load() != 1 || out != "" {
		t.Fatalf("authentication service failure repeated or swallowed: err=%v metadata=%d other=%d CDN=%d output=%q", err, metadata.Load(), otherRequests.Load(), cdnRequests.Load(), out)
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
		if r.URL.Path == "/api/v2/maintenance/events:batch" {
			writeV2RecoveryData(w, map[string]any{})
			return
		}
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
