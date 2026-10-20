package cmd

import (
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
	"github.com/spf13/cobra"
)

func TestACPMainSessionSelectionNeverBorrowsCallingCodexThread(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "unrelated-codex-thread")
	for _, host := range []string{"claude-code", "openclaw", "hermes", "workbuddy", "third-party"} {
		for _, selector := range []string{"explicit", "current", "scheduler", "codex-home"} {
			t.Run(host+"/"+selector, func(t *testing.T) {
				cmd := &cobra.Command{}
				cmd.Flags().String("main-session-id", "", "")
				cmd.Flags().Bool("current-session", false, "")
				cmd.Flags().Bool("scheduler-stdin", false, "")
				cmd.Flags().String("scheduler-json", "", "")
				cmd.Flags().String("scheduler-file", "", "")
				cmd.Flags().String("codex-home", "", "")
				switch selector {
				case "current":
					_ = cmd.Flags().Set("current-session", "true")
				case "scheduler":
					_ = cmd.Flags().Set("scheduler-json", "{}")
				default:
					_ = cmd.Flags().Set("main-session-id", "explicit-owner-session")
				}
				if selector == "codex-home" {
					_ = cmd.Flags().Set("codex-home", t.TempDir())
				}
				binding := dispatch.Binding{Host: host, Mode: "acp"}
				target, err := selectMainSession(cmd, &binding)
				if selector == "explicit" {
					if err != nil || target.SessionID != "explicit-owner-session" || len(binding.Env) != 0 {
						t.Fatalf("unexpected target: %+v %v", target, err)
					}
				} else if err == nil {
					t.Fatal("ACP binding inherited Codex-only selection")
				}
			})
		}
	}
}
