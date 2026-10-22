package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/watchservice"
	"cli.eigenflux.ai/internal/workbuddy"
	"github.com/spf13/cobra"
)

func TestWorkBuddyAutomaticSetup(t *testing.T) {
	for _, scenario := range []string{"success", "probe_failed", "foreign_task", "other_binding", "identity_changed", "restart_failed", "existing_workbuddy"} {
		t.Run(scenario, func(t *testing.T) {
			w := newCommissionWatch(t, "https://example.test", "commission_order")
			existing := *w.binding
			if scenario == "existing_workbuddy" {
				existing.Host, existing.Mode = "workbuddy", "acp"
				dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), existing)
			} else if scenario != "other_binding" {
				os.Remove(dispatch.BindingPath(w.home, w.server.Name))
			}
			if scenario == "identity_changed" {
				existing.AgentID = "other"
				dispatch.WriteJSON(notificationWatchPath(w.home, w.server.Name), existing)
			}
			var calls []string
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			cmd.SetOut(&bytes.Buffer{})
			deps := workBuddySetupDeps{
				rules: func() (string, error) { return t.TempDir(), nil },
				discover: func(workbuddy.Config) (workbuddy.Config, error) {
					calls = append(calls, "discover")
					return workbuddy.Config{ConfigDir: t.TempDir(), CLIPath: filepath.Join(t.TempDir(), "codebuddy"), Node: os.Args[0]}, nil
				},
				probe: func(_ context.Context, c workbuddy.Config, dir string) error {
					calls = append(calls, "probe")
					if len(c.AllowedTools) != 4 {
						t.Fatal("missing explicit tools")
					}
					if scenario == "probe_failed" {
						return errors.New("host unavailable")
					}
					return nil
				},
				start: func(_ context.Context, task watchservice.NotificationTask) (watchservice.Result, error) {
					if !task.Dispatch || task.AgentID != "42" || task.Home != w.home {
						t.Fatal("wrong task routing")
					}
					if task.PrepareOnly {
						calls = append(calls, "stop")
						if scenario == "foreign_task" {
							return watchservice.Result{}, errors.New("task ownership")
						}
					} else {
						calls = append(calls, "start")
						if scenario == "restart_failed" {
							return watchservice.Result{}, errors.New("start failure")
						}
					}
					return watchservice.Result{Status: "process_started"}, nil
				},
			}
			err := setupWorkBuddyWatch(cmd, deps)
			wantOK := scenario == "success" || scenario == "existing_workbuddy"
			if (err == nil) != wantOK {
				t.Fatalf("err=%v", err)
			}
			b, readErr := dispatch.ReadBinding(w.home, w.server.Name)
			if wantOK || scenario == "restart_failed" {
				if readErr != nil || b.Host != "workbuddy" || b.Mode != "acp" || !b.Handles("commission_order") || !strings.Contains(strings.Join(b.Command, " "), "workbuddy-acp") {
					t.Fatalf("binding=%+v err=%v", b, readErr)
				}
				if strings.Join(calls, ",") != "discover,probe,stop,start" {
					t.Fatal(calls)
				}
				if scenario == "existing_workbuddy" && b.Revision != existing.Revision {
					t.Fatal("lost pending job scope")
				}
			} else if scenario != "other_binding" && !os.IsNotExist(readErr) {
				t.Fatalf("failed setup wrote binding: %v", readErr)
			}
			if scenario == "other_binding" && len(calls) > 0 {
				t.Fatal("took over another host")
			}
		})
	}
}

func TestWorkBuddySetupPinsIdentityAfterHostProbe(t *testing.T) {
	w := newCommissionWatch(t, "https://example.test", "commission_order")
	os.Remove(dispatch.BindingPath(w.home, w.server.Name))
	deps := workBuddySetupDeps{rules: func() (string, error) { return t.TempDir(), nil }, discover: func(c workbuddy.Config) (workbuddy.Config, error) { return c, nil }, probe: func(context.Context, workbuddy.Config, string) error {
		cred, _ := auth.LoadV2Credentials(w.server.Name)
		cred.AgentID = "changed"
		return auth.SaveV2Credentials(w.server.Name, cred)
	}, start: func(context.Context, watchservice.NotificationTask) (watchservice.Result, error) {
		return watchservice.Result{}, nil
	}}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := setupWorkBuddyWatch(cmd, deps); err == nil {
		t.Fatal("identity change accepted")
	}
	if _, err := os.Stat(dispatch.BindingPath(w.home, w.server.Name)); !os.IsNotExist(err) {
		t.Fatal("wrote changed identity")
	}
}
