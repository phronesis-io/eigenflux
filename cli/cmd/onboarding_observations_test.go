package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/installflow"
)

func TestOnboardingObservationAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		code                  int
		wrongSession, unbound bool
		ok                    bool
	}{
		{name: "accepted", ok: true}, {name: "replayed", ok: true}, {name: "business rejection", code: 1}, {name: "wrong session", wrongSession: true}, {name: "unbound authenticated", unbound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempHome(t)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b installflow.Batch
				if json.NewDecoder(r.Body).Decode(&b) != nil {
					t.Fatal("invalid batch")
				}
				if r.URL.Path != "/api/v2/agents/me/install-telemetry/events:batch" || r.Header.Get("Authorization") != "Bearer test-token" || len(r.Header.Get("X-Install-Session-Proof")) != 64 {
					t.Error("missing authenticated scope")
				}
				if tc.wrongSession {
					b.SessionID = "wrong"
				}
				w.WriteHeader(202)
				fmt.Fprintf(w, `{"code":%d,"data":{"accepted_events":0,"install_session_id":%q,"bound":%t}}`, tc.code, b.SessionID, !tc.unbound)
			}))
			defer endpoint.Close()
			server := config.Server{Endpoint: endpoint.URL}
			session, err := installflow.Start(installScope(server), false)
			if err != nil {
				t.Fatal(err)
			}
			event, err := installflow.RecordForAgent(installScope(server), "agent-1", installEvent("credentials_saved", "succeeded"))
			if err != nil {
				t.Fatal(err)
			}
			err = flushInstallObservations(server, "agent-1", "test-token", event.ID)
			if (err == nil) != tc.ok {
				t.Fatalf("ack error=%v, want success=%v", err, tc.ok)
			}
			dir, _ := installflow.Directory(installScope(server))
			_, err = os.Stat(filepath.Join(dir, session.ID, event.ID+".json"))
			if os.IsNotExist(err) != tc.ok {
				t.Fatal("queue acknowledgement mismatch", err)
			}
		})
	}
}

func TestOnboardingObservationDoesNotConfirmUnsentEvent(t *testing.T) {
	tempHome(t)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b installflow.Batch
		_ = json.NewDecoder(r.Body).Decode(&b)
		if len(b.Events) != 20 {
			t.Errorf("batch size=%d", len(b.Events))
		}
		w.WriteHeader(202)
		fmt.Fprintf(w, `{"code":0,"data":{"accepted_events":20,"install_session_id":%q,"bound":false}}`, b.SessionID)
	}))
	defer endpoint.Close()
	server := config.Server{Endpoint: endpoint.URL}
	if _, err := installflow.Start(installScope(server), false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := installflow.Record(installScope(server), installflow.Event{ID: fmt.Sprintf("%032x", i), Type: "host_detected", Result: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	target, err := installflow.Record(installScope(server), installflow.Event{ID: strings.Repeat("f", 32), Type: "host_detected", Result: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err = flushInstallObservations(server, "", "", target.ID); err == nil {
		t.Fatal("unsent target reported as confirmed")
	}
}

func TestOnboardingProofNeverFollowsRedirect(t *testing.T) {
	tempHome(t)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("capability sent to redirected destination")
		w.WriteHeader(202)
	}))
	defer destination.Close()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer endpoint.Close()
	server := config.Server{Endpoint: endpoint.URL}
	if _, err := installflow.Start(installScope(server), false); err != nil {
		t.Fatal(err)
	}
	if _, err := installflow.Record(installScope(server), installEvent("host_detected", "pending")); err != nil {
		t.Fatal(err)
	}
	if err := flushInstallObservations(server, "", ""); err == nil {
		t.Fatal("redirect accepted")
	}
	p := installationPoster{client: client.New(endpoint.URL, "", version, client.Meta{}), server: server}
	if _, err := p.Post("/provision", map[string]string{}); err == nil {
		t.Fatal("provision redirect accepted")
	}
}

func TestOnboardingObservationOptOutCreatesNothing(t *testing.T) {
	tempHome(t)
	t.Setenv("EIGENFLUX_INSTALL_OBSERVATIONS", "0")
	for _, start := range []bool{true, false} {
		cmd := newOnboardingObserveCommand()
		if start {
			cmd = newOnboardingStartCommand()
		}
		var b bytes.Buffer
		cmd.SetOut(&b)
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "disabled") {
			t.Fatal("opt-out status missing")
		}
	}
	if _, err := os.Stat(filepath.Join(config.HomeDir(), "install-observations")); !os.IsNotExist(err) {
		t.Fatal("opt-out wrote state", err)
	}
	if err := flushInstallObservations(config.Server{Endpoint: "http://127.0.0.1:1"}, "", ""); err == nil {
		t.Fatal("opt-out claimed upload")
	}
}

func TestOnboardingObservationFreshBaseline(t *testing.T) {
	for _, tc := range []struct {
		name, mode, state, items string
		want                     int
	}{
		{"empty success", "baseline", "in_progress", "[]", 1},
		{"nonempty success", "baseline", "in_progress", "[{}]", 1},
		{"completed fallback", "baseline", "completed", "[]", 0},
		{"personalized", "intent_aligned", "completed", "[]", 0},
		{"unknown state", "baseline", "", "[]", 0},
		{"missing items", "baseline", "in_progress", "null", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b installflow.Batch
				if json.NewDecoder(r.Body).Decode(&b) != nil {
					t.Error("invalid batch")
				}
				for _, e := range b.Events {
					if e.Type != "baseline_feed" || e.Result != "succeeded" || e.Count == nil {
						t.Error("invalid baseline evidence")
					}
					seen++
				}
				w.WriteHeader(202)
				fmt.Fprintf(w, `{"code":0,"data":{"accepted_events":%d,"install_session_id":%q,"bound":true}}`, len(b.Events), b.SessionID)
			}))
			defer endpoint.Close()
			cfg, name := runtimeTestConfig(t, endpoint.URL, true)
			server, err := cfg.GetActive(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = installflow.Start(installScope(*server), false); err != nil {
				t.Fatal(err)
			}
			payload := json.RawMessage(fmt.Sprintf(`{"schema_version":"feed.v2","personalization":{"mode":%q,"onboarding_state":%q},"items":%s}`, tc.mode, tc.state, tc.items))
			observeBaselineFeed(*server, payload)
			if seen != tc.want {
				t.Fatalf("observations=%d, want %d", seen, tc.want)
			}
		})
	}
}

func TestOnboardingObservationContextCorrelation(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/agent-context" {
					t.Error("unexpected route")
				}
				hasProof := r.Header.Get("X-Install-Session-Proof") != ""
				if hasProof == mismatch {
					t.Error("incorrect account/session correlation")
				}
				fmt.Fprint(w, `{"code":0,"data":{"context_revision":1}}`)
			}))
			defer endpoint.Close()
			cfg, name := runtimeTestConfig(t, endpoint.URL, true)
			server, _ := cfg.GetActive(name)
			if _, err := installflow.Start(installScope(*server), false); err != nil {
				t.Fatal(err)
			}
			if mismatch {
				if _, err := installflow.RecordForAgent(installScope(*server), "other-agent", installEvent("credentials_saved", "succeeded")); err != nil {
					t.Fatal(err)
				}
			}
			c := client.New(endpoint.URL+"/api/v2", "test-v2", version, client.Meta{})
			if _, err := getInstallContext(c, *server, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
