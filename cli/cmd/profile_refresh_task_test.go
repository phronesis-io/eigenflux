package cmd

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
	"github.com/spf13/cobra"
)

func profileTaskFixture(t *testing.T, baseline bool) (string, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/agent-context" {
			t.Errorf("unexpected task request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if baseline {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"ONBOARDING_REQUIRED","details":{"onboarding_state":"in_progress"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"context_revision":1}}`))
	}))
	t.Cleanup(server.Close)
	_, serverName := runtimeTestConfig(t, server.URL, true)
	installHeartbeatTestRules(t)
	oldFormat := formatFlag
	formatFlag = "agent"
	t.Cleanup(func() { formatFlag = oldFormat })
	return config.HomeDir(), serverName
}

func runProfileTask(output io.Writer, dirs, snippets []string, force ...bool) error {
	command := &cobra.Command{}
	command.SetOut(output)
	command.Flags().Bool("force", len(force) > 0 && force[0], "")
	command.Flags().StringArray("memory-dir", dirs, "")
	command.Flags().StringArray("session-snippet", snippets, "")
	return profileRefreshTaskCmd.RunE(command, nil)
}

func TestProfileRefreshTaskForceBypassesFreshness(t *testing.T) {
	now := time.Now().Unix()
	for _, tc := range []struct {
		name  string
		state profilestate.State
	}{
		{"first seen", profilestate.State{}},
		{"recently completed", profilestate.State{LastCheckedUnix: now - 10}},
		{"inside cooldown", profilestate.State{LastRefreshUnix: now - 10, LastPromptedUnix: now - 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, server := profileTaskFixture(t, false)
			if err := profilestate.Save(home, server, "agent-1", tc.state); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := runProfileTask(&out, nil, nil, true); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(out.String(), "EIGENFLUX PROFILE REVIEW TASK\n") {
				t.Fatalf("explicit refresh did not deliver immediately: %q", out.String())
			}
			if state := profilestate.Load(home, server, "agent-1"); state.LastPromptedUnix < now {
				t.Fatalf("explicit task delivery was not finalized: %+v", state)
			}
		})
	}
}

func TestProfileRefreshTaskForceDoesNotBypassOnboarding(t *testing.T) {
	home, server := profileTaskFixture(t, true)
	before := profilestate.State{LastRefreshUnix: time.Now().Add(-48 * time.Hour).Unix()}
	if err := profilestate.Save(home, server, "agent-1", before); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runProfileTask(&out, nil, nil, true)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict || apiErr.ErrorCode != "ONBOARDING_REQUIRED" {
		t.Fatalf("forced baseline refresh lost the typed onboarding restriction: %v", err)
	}
	if out.Len() != 0 || profilestate.Load(home, server, "agent-1") != before {
		t.Fatalf("forced baseline refresh emitted or claimed work: %q", out.String())
	}
}

func TestProfileRefreshTaskForceDoesNotBypassAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"AGENT_CREDENTIAL_REVOKED","message":"credential revoked"}}`))
	}))
	defer server.Close()
	_, serverName := runtimeTestConfig(t, server.URL, true)
	installHeartbeatTestRules(t)
	oldFormat := formatFlag
	formatFlag = "agent"
	t.Cleanup(func() { formatFlag = oldFormat })
	before := profilestate.State{LastRefreshUnix: time.Now().Add(-48 * time.Hour).Unix()}
	if err := profilestate.Save(config.HomeDir(), serverName, "agent-1", before); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runProfileTask(&out, nil, nil, true)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized || apiErr.ErrorCode != "AGENT_CREDENTIAL_REVOKED" {
		t.Fatalf("forced refresh lost the credential diagnostic: %v", err)
	}
	if out.Len() != 0 || profilestate.Load(config.HomeDir(), serverName, "agent-1") != before {
		t.Fatalf("forced unauthenticated refresh emitted or claimed work: %q", out.String())
	}
}

func TestProfileRefreshTaskCentralDueAndCooldown(t *testing.T) {
	now := time.Now().Unix()
	day, hour := int64(24*time.Hour/time.Second), int64(time.Hour/time.Second)
	for _, tc := range []struct {
		name  string
		state profilestate.State
		ready bool
	}{
		{"first seen seeds", profilestate.State{}, false},
		{"inside 24 hours", profilestate.State{LastRefreshUnix: now - day + 60}, false},
		{"due after 24 hours", profilestate.State{LastRefreshUnix: now - day - 60}, true},
		{"recent review settles old profile", profilestate.State{LastRefreshUnix: now - day - 60, LastCheckedUnix: now - 60}, false},
		{"inside prompt cooldown", profilestate.State{LastRefreshUnix: now - day - 60, LastPromptedUnix: now - hour + 60}, false},
		{"expired prompt cooldown", profilestate.State{LastRefreshUnix: now - day - 60, LastPromptedUnix: now - hour - 60}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, server := profileTaskFixture(t, false)
			if err := profilestate.Save(home, server, "agent-1", tc.state); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := runProfileTask(&out, nil, nil); err != nil {
				t.Fatal(err)
			}
			if (out.Len() > 0) != tc.ready {
				t.Fatalf("ready=%t output=%q", tc.ready, out.String())
			}
			state := profilestate.Load(home, server, "agent-1")
			if tc.ready {
				if !strings.HasPrefix(out.String(), "EIGENFLUX PROFILE REVIEW TASK\n") || state.LastPromptedUnix < now {
					t.Fatalf("task delivery was not finalized: output=%q state=%+v", out.String(), state)
				}
			} else if tc.state == (profilestate.State{}) && state.LastCheckedUnix < now {
				t.Fatalf("first observation did not seed state: %+v", state)
			}
		})
	}
}

func TestProfileRefreshTaskBaselineIsSilentAndPreservesState(t *testing.T) {
	home, server := profileTaskFixture(t, true)
	overdue := profilestate.State{LastRefreshUnix: time.Now().Add(-48 * time.Hour).Unix()}
	if err := profilestate.Save(home, server, "agent-1", overdue); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runProfileTask(&out, nil, nil); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || profilestate.Load(home, server, "agent-1") != overdue {
		t.Fatalf("baseline emitted or claimed profile work: %q", out.String())
	}
}

func TestExplicitProfileRefreshTaskRunsForPluginAndPreservesHostContext(t *testing.T) {
	home, server := profileTaskFixture(t, false)
	clientMeta = client.Meta{Host: "claude-code/1.0", Mode: "plugin"}
	if err := profilestate.Save(home, server, "agent-1", profilestate.State{LastCheckedUnix: time.Now().Add(-48 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	memoryDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(memoryDir, "CLAUDE.md"), []byte("Host context fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runProfileTask(&out, []string{memoryDir}, []string{"Recent host session fixture"}); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"EIGENFLUX PROFILE REVIEW TASK", "Host context fixture", "Recent host session fixture", "--homedir " + shellQuote(home), "--server " + shellQuote(server), filepath.Join(os.Getenv("EIGENFLUX_SKILLS_DIR"), "ef-profile", "SKILL.md")} {
		if !strings.Contains(out.String(), required) {
			t.Fatalf("task dropped %q: %s", required, out.String())
		}
	}
}

func TestProfileRefreshTaskMissingRuleDoesNotClaimWork(t *testing.T) {
	home, server := profileTaskFixture(t, false)
	before := profilestate.State{LastRefreshUnix: time.Now().Add(-48 * time.Hour).Unix()}
	if err := profilestate.Save(home, server, "agent-1", before); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(os.Getenv("EIGENFLUX_SKILLS_DIR"), "ef-profile", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runProfileTask(&out, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "required rule source") || out.Len() != 0 {
		t.Fatalf("missing rule did not fail explicitly: err=%v output=%q", err, out.String())
	}
	if profilestate.Load(home, server, "agent-1") != before {
		t.Fatal("missing rule consumed task cooldown")
	}
}

func TestProfileRefreshTaskConcurrentHeartbeatsDeliverOnce(t *testing.T) {
	home, server := profileTaskFixture(t, false)
	if err := profilestate.Save(home, server, "agent-1", profilestate.State{LastRefreshUnix: time.Now().Add(-48 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	var delivered atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			var out bytes.Buffer
			if err := runProfileTask(&out, nil, nil); err != nil {
				t.Error(err)
				return
			}
			if out.Len() > 0 {
				delivered.Add(1)
			}
		}()
	}
	workers.Wait()
	if got := delivered.Load(); got != 1 {
		t.Fatalf("concurrent heartbeats delivered %d tasks", got)
	}
}

func TestProfileRefreshTaskAccountChangeDoesNotReuseCooldown(t *testing.T) {
	home, server := profileTaskFixture(t, false)
	overdue := profilestate.State{LastRefreshUnix: time.Now().Add(-48 * time.Hour).Unix()}
	for _, agentID := range []string{"agent-1", "agent-2"} {
		if err := profilestate.Save(home, server, agentID, overdue); err != nil {
			t.Fatal(err)
		}
		if err := auth.SaveV2Credentials(server, &auth.V2Credentials{AgentID: agentID, AccessToken: "test-v2", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runProfileTask(&out, nil, nil); err != nil {
			t.Fatal(err)
		}
		if out.Len() == 0 || profilestate.Load(home, server, agentID).LastPromptedUnix == 0 {
			t.Fatalf("account %s lost its due task", agentID)
		}
	}
}

type failedProfileTaskOutput struct{}

func (failedProfileTaskOutput) Write([]byte) (int, error) { return 0, errors.New("host pipe closed") }

func TestProfileRefreshTaskFailedOutputKeepsOnlyShortLease(t *testing.T) {
	home, server := profileTaskFixture(t, false)
	now := time.Now().Unix()
	if err := profilestate.Save(home, server, "agent-1", profilestate.State{LastRefreshUnix: now - int64(48*time.Hour/time.Second)}); err != nil {
		t.Fatal(err)
	}
	err := runProfileTask(failedProfileTaskOutput{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "host pipe closed") {
		t.Fatalf("output failure lost: %v", err)
	}
	got := profilestate.Load(home, server, "agent-1").LastPromptedUnix
	want := now - int64((profilePromptCooldown-profilePromptClaimLease)/time.Second)
	if got < want-1 || got > want+1 {
		t.Fatalf("failed output stored long cooldown: got %d, want near %d", got, want)
	}
}

type profileTaskWriterFunc func([]byte) (int, error)

func (write profileTaskWriterFunc) Write(data []byte) (int, error) { return write(data) }

func TestProfileRefreshTaskFinalizationDoesNotOverwriteCompletion(t *testing.T) {
	home, server := profileTaskFixture(t, false)
	if err := profilestate.Save(home, server, "agent-1", profilestate.State{LastRefreshUnix: time.Now().Add(-48 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	writer := profileTaskWriterFunc(func(data []byte) (int, error) {
		if err := stampProfileRefreshKeyFor(server, "agent-1", kvProfileRefreshCheckedAt); err != nil {
			return 0, err
		}
		return len(data), nil
	})
	if err := runProfileTask(writer, nil, nil); err != nil {
		t.Fatal(err)
	}
	state := profilestate.Load(home, server, "agent-1")
	if state.LastCheckedUnix <= 0 || state.LastPromptedUnix != 0 {
		t.Fatalf("late delivery finalization overwrote completed review: %+v", state)
	}
}

func TestProfileRefreshTaskCommandIsRegistered(t *testing.T) {
	if profileRefreshTaskCmd.Parent() != profileCmd {
		t.Fatal("refresh-task is not available under profile")
	}
}
