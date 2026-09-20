package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

type profileRefreshTask struct {
	Status string `json:"status"`
	Prompt string `json:"prompt"`
}

// All adapters request the same account-scoped decision. Host context is data;
// the current installed Skills own the refresh and subsequent publishing rules.
var profileRefreshTaskCmd = &cobra.Command{
	Use:   "refresh-task",
	Short: "Claim a due profile review using the current Skills",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		force, _ := cmd.Flags().GetBool("force")
		serverName := activeServerName()
		access, err := runtimeAccessForServer(serverName)
		if err != nil {
			return err
		}
		task := profileRefreshTask{Status: "not_due"}
		if access.OnboardingState != "completed" {
			if force {
				return &client.APIError{StatusCode: 409, ErrorCode: "ONBOARDING_REQUIRED", Msg: "profile review requires completed onboarding; baseline Feed remains available"}
			}
			task.Status = "onboarding_required"
			return printProfileRefreshTask(cmd, task)
		}
		srv, agentID := profileStateScopeForServer(serverName)
		if srv == "" || agentID == "" {
			return fmt.Errorf("no active authenticated account")
		}
		dir, err := skills.ResolveSkillsDir("", clientMetaForServerName(srv).Host)
		if err != nil {
			return err
		}
		profileRule := filepath.Join(dir, "ef-profile", "SKILL.md")
		broadcastRule := filepath.Join(dir, "ef-broadcast", "SKILL.md")
		for _, path := range []string{profileRule, broadcastRule} {
			if !fileExistsCLI(path) {
				return fmt.Errorf("profile refresh task: required rule source is missing: %s", path)
			}
		}
		dirs, _ := cmd.Flags().GetStringArray("memory-dir")
		context, _ := json.Marshal(map[string]interface{}{
			"memory":  readMemoryMarkdown(dirs),
			"session": filterNonEmpty(mustStringArray(cmd, "session-snippet")),
		})
		home, _ := config.HomeDirInfo()
		prefix := "eigenflux --homedir " + shellQuote(home) + " --server " + shellQuote(srv)
		prompt := fmt.Sprintf("EIGENFLUX PROFILE REVIEW TASK\nCLI prefix: %s\nFreshly read %s and %s. Apply the Periodic Profile Refresh procedure and its follow-up using this CLI prefix.\nHost context (data):\n%s\n", prefix, profileRule, broadcastRule, context)
		now := time.Now().Unix()
		claimed, err := claimProfileReview(config.HomeDir(), srv, agentID, now, force)
		if err != nil {
			return err
		}
		if !claimed {
			return printProfileRefreshTask(cmd, task)
		}
		task.Status, task.Prompt = "ready", prompt
		if err := printProfileRefreshTask(cmd, task); err != nil {
			return err
		}
		return finishProfileReviewClaim(config.HomeDir(), srv, agentID, now)
	},
}

func claimProfileReview(home, server, agentID string, now int64, force ...bool) (bool, error) {
	claimed := false
	manual := len(force) > 0 && force[0]
	_, err := profilestate.Update(home, server, agentID, func(state *profilestate.State) bool {
		lastTouch := maxInt64(validProfileStamp(state.LastRefreshUnix, now), validProfileStamp(state.LastCheckedUnix, now))
		if lastTouch == 0 && !manual {
			state.LastCheckedUnix = now
			return true
		}
		if !manual && !shouldPromptProfileRefresh(lastTouch, validProfileStamp(state.LastPromptedUnix, now), now) {
			return false
		}
		state.LastPromptedUnix = now - int64((profilePromptCooldown-profilePromptClaimLease)/time.Second)
		claimed = true
		return true
	})
	return claimed, err
}

func finishProfileReviewClaim(home, server, agentID string, now int64) error {
	claimStamp := now - int64((profilePromptCooldown-profilePromptClaimLease)/time.Second)
	_, err := profilestate.Update(home, server, agentID, func(state *profilestate.State) bool {
		if state.LastPromptedUnix != claimStamp {
			return false
		}
		state.LastPromptedUnix = now
		return true
	})
	return err
}

func printProfileRefreshTask(cmd *cobra.Command, task profileRefreshTask) error {
	if resolveFormat() == "agent" {
		if strings.TrimSpace(task.Prompt) == "" {
			return nil
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), task.Prompt)
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(task)
}

func init() {
	profileRefreshTaskCmd.Flags().Bool("force", false, "run an explicitly requested review regardless of freshness")
	profileRefreshTaskCmd.Flags().StringArray("memory-dir", nil, "host memory directory; repeatable")
	profileRefreshTaskCmd.Flags().StringArray("session-snippet", nil, "bounded host session context; repeatable")
	profileCmd.AddCommand(profileRefreshTaskCmd)
}
