package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/controlcontext"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

type cliIntentAction struct {
	IntentID          string `json:"intent_id"`
	WatchFor          string `json:"watch_for"`
	TriggerWhen       string `json:"trigger_when"`
	ActionInstruction string `json:"then"`
	ActionPolicy      string `json:"action_policy"`
	Priority          int16  `json:"priority"`
	Status            string `json:"status"`
}

type cliSecurityBoundary struct {
	RecurringPublish bool `json:"recurring_publish"`
	AutoReplyPM      bool `json:"auto_reply_pm"`
	AutoComment      bool `json:"auto_comment"`
	ShowAddFriend    bool `json:"show_add_friend"`
}

type cliControlContext struct {
	ContextRevision int64 `json:"context_revision"`
	NetworkGoal     struct {
		GoalID string `json:"goal_id"`
		Text   string `json:"text"`
	} `json:"network_goal"`
	IntentActions    []cliIntentAction   `json:"intent_actions"`
	SecurityBoundary cliSecurityBoundary `json:"security_boundary"`
}

func freshControlContext(clientV2 *client.Client, serverName, ownerAgentID string, persist bool) (cliControlContext, error) {
	response, err := clientV2.Get("/agent-context", map[string]string{"if_newer": "0"})
	if err != nil {
		return cliControlContext{}, err
	}
	var envelope struct {
		ContextRevision int64           `json:"context_revision"`
		ControlContext  json.RawMessage `json:"control_context"`
	}
	if json.Unmarshal(response.Data, &envelope) != nil || envelope.ContextRevision <= 0 || len(envelope.ControlContext) == 0 {
		return cliControlContext{}, fmt.Errorf("invalid control-context response")
	}
	var current cliControlContext
	if json.Unmarshal(envelope.ControlContext, &current) != nil {
		return cliControlContext{}, fmt.Errorf("invalid control-context payload")
	}
	current.ContextRevision = envelope.ContextRevision
	if persist {
		if err := controlcontext.Save(serverName, controlcontext.Snapshot{
			OwnerAgentID: ownerAgentID, Revision: envelope.ContextRevision, Context: envelope.ControlContext,
		}); err != nil {
			return cliControlContext{}, err
		}
	}
	return current, nil
}

func contextMutationKey(prefix string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(raw), nil
}

func contextMutationClient() (*client.Client, string, string, cliControlContext, error) {
	clientV2, server, err := newV2ClientForServer(serverFlag, true)
	if err != nil {
		return nil, "", "", cliControlContext{}, err
	}
	credentials, err := auth.LoadV2Credentials(server.Name)
	if err != nil {
		return nil, "", "", cliControlContext{}, err
	}
	current, err := freshControlContext(clientV2, server.Name, credentials.AgentID, false)
	return clientV2, server.Name, credentials.AgentID, current, err
}

func finishContextMutation(clientV2 *client.Client, serverName, ownerAgentID string, response *client.APIResponse, syncSecurity bool) error {
	var result map[string]interface{}
	if json.Unmarshal(response.Data, &result) != nil {
		return fmt.Errorf("invalid context mutation response")
	}
	output.PrintData(result, resolveFormat())
	current, err := freshControlContext(clientV2, serverName, ownerAgentID, true)
	if err != nil {
		return fmt.Errorf("remote mutation committed; do not repeat it; run 'eigenflux context pull' to refresh local state: %w", err)
	}
	if syncSecurity {
		if err := syncLocalSecurityBoundary(current.SecurityBoundary); err != nil {
			return fmt.Errorf("remote mutation committed; do not repeat it; run 'eigenflux context pull' to refresh local state: %w", err)
		}
	}
	return nil
}

func syncLocalSecurityBoundary(boundary cliSecurityBoundary) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	values := map[string]bool{
		"recurring_publish": boundary.RecurringPublish,
		"auto_reply_pm":     boundary.AutoReplyPM,
		"auto_comment":      boundary.AutoComment,
		"show_add_friend":   boundary.ShowAddFriend,
	}
	changed := false
	for key, value := range values {
		changed = setConfigKVInMemory(cfg, key, strconv.FormatBool(value)) || changed
	}
	if changed {
		return cfg.Save()
	}
	return nil
}

func contextMutationError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 409 {
		return fmt.Errorf("control context changed; run 'eigenflux context pull', re-evaluate the requested change, and retry")
	}
	return err
}

var contextGoalCmd = &cobra.Command{Use: "goal", Short: "Manage the network goal"}

var contextGoalSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set the owner-confirmed network goal",
	RunE: func(cmd *cobra.Command, _ []string) error {
		text, _ := cmd.Flags().GetString("text")
		text = strings.TrimSpace(text)
		if text == "" {
			return fmt.Errorf("--text is required")
		}
		clientV2, serverName, ownerAgentID, current, err := contextMutationClient()
		if err != nil {
			return err
		}
		key, err := contextMutationKey("cli-goal")
		if err != nil {
			return err
		}
		response, err := clientV2.Put("/agent-context/network-goal", map[string]interface{}{
			"goal_text": text, "expected_context_revision": current.ContextRevision, "idempotency_key": key,
		})
		if err != nil {
			return contextMutationError(err)
		}
		return finishContextMutation(clientV2, serverName, ownerAgentID, response, false)
	},
}

var contextIntentCmd = &cobra.Command{Use: "intent", Short: "Manage intents and actions"}

var contextIntentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the latest owner-confirmed intents and actions",
	RunE: func(_ *cobra.Command, _ []string) error {
		_, _, _, current, err := contextMutationClient()
		if err != nil {
			return err
		}
		output.PrintData(map[string]interface{}{
			"context_revision": current.ContextRevision, "intent_actions": current.IntentActions,
		}, resolveFormat())
		return nil
	},
}

func intentFields(cmd *cobra.Command, defaults *cliIntentAction) (map[string]interface{}, string, error) {
	watchFor, _ := cmd.Flags().GetString("watch-for")
	triggerWhen, _ := cmd.Flags().GetString("trigger-when")
	actionInstruction, _ := cmd.Flags().GetString("action-instruction")
	actionPolicy, _ := cmd.Flags().GetString("action-policy")
	priority, _ := cmd.Flags().GetInt16("priority")
	if defaults != nil {
		if !cmd.Flags().Changed("watch-for") {
			watchFor = defaults.WatchFor
		}
		if !cmd.Flags().Changed("trigger-when") {
			triggerWhen = defaults.TriggerWhen
		}
		if !cmd.Flags().Changed("action-instruction") {
			actionInstruction = defaults.ActionInstruction
		}
		if !cmd.Flags().Changed("action-policy") {
			actionPolicy = defaults.ActionPolicy
		}
		if !cmd.Flags().Changed("priority") {
			priority = defaults.Priority
		}
	}
	if strings.TrimSpace(watchFor) == "" {
		return nil, "", fmt.Errorf("--watch-for is required")
	}
	if actionPolicy != "analyze_only" && actionPolicy != "draft" && actionPolicy != "network_action" && actionPolicy != "trade_action" {
		return nil, "", fmt.Errorf("--action-policy must be analyze_only, draft, network_action, or trade_action")
	}
	confirmed, _ := cmd.Flags().GetBool("confirm-elevated-policy")
	if (actionPolicy == "network_action" || actionPolicy == "trade_action") && !confirmed {
		return nil, "", fmt.Errorf("%s requires fresh human confirmation; retry with --confirm-elevated-policy only after the user explicitly approves it", actionPolicy)
	}
	return map[string]interface{}{
		"watch_for": strings.TrimSpace(watchFor), "trigger_when": strings.TrimSpace(triggerWhen),
		"action_instruction": strings.TrimSpace(actionInstruction), "action_policy": actionPolicy, "priority": priority,
	}, actionPolicy, nil
}

func addIntentFlags(command *cobra.Command) {
	command.Flags().String("watch-for", "", "information the Agent should keep watching")
	command.Flags().String("trigger-when", "", "observable condition for further handling")
	command.Flags().String("action-instruction", "", "bounded instruction for what the Agent should do next")
	command.Flags().String("action-policy", "analyze_only", "analyze_only|draft|network_action|trade_action")
	command.Flags().Int16("priority", 0, "intent priority")
	command.Flags().Bool("confirm-elevated-policy", false, "confirm fresh human approval for network_action or trade_action")
}

var contextIntentAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add an owner-confirmed intent and action",
	RunE: func(cmd *cobra.Command, _ []string) error {
		fields, _, err := intentFields(cmd, nil)
		if err != nil {
			return err
		}
		clientV2, serverName, ownerAgentID, current, err := contextMutationClient()
		if err != nil {
			return err
		}
		key, err := contextMutationKey("cli-intent-add")
		if err != nil {
			return err
		}
		fields["expected_context_revision"], fields["idempotency_key"] = current.ContextRevision, key
		response, err := clientV2.Post("/agent-context/intent-actions", fields)
		if err != nil {
			return contextMutationError(err)
		}
		return finishContextMutation(clientV2, serverName, ownerAgentID, response, false)
	},
}

var contextIntentUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update one owner-confirmed intent and action",
	RunE: func(cmd *cobra.Command, _ []string) error {
		intentID, _ := cmd.Flags().GetString("intent-id")
		if parsed, err := strconv.ParseInt(intentID, 10, 64); err != nil || parsed <= 0 {
			return fmt.Errorf("--intent-id must be a positive integer")
		}
		clientV2, serverName, ownerAgentID, current, err := contextMutationClient()
		if err != nil {
			return err
		}
		var existing *cliIntentAction
		for _, intent := range current.IntentActions {
			if intent.IntentID == intentID {
				copy := intent
				existing = &copy
				break
			}
		}
		if existing == nil {
			return fmt.Errorf("intent %s is not active", intentID)
		}
		changed := cmd.Flags().Changed("watch-for") || cmd.Flags().Changed("trigger-when") ||
			cmd.Flags().Changed("action-instruction") || cmd.Flags().Changed("action-policy") ||
			cmd.Flags().Changed("priority")
		if !changed {
			return fmt.Errorf("provide at least one intent field to update")
		}
		fields, _, err := intentFields(cmd, existing)
		if err != nil {
			return err
		}
		key, err := contextMutationKey("cli-intent-update")
		if err != nil {
			return err
		}
		fields["expected_context_revision"], fields["idempotency_key"] = current.ContextRevision, key
		response, err := clientV2.Put("/agent-context/intent-actions/"+intentID, fields)
		if err != nil {
			return contextMutationError(err)
		}
		return finishContextMutation(clientV2, serverName, ownerAgentID, response, false)
	},
}

var contextIntentDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete one owner-confirmed intent and action",
	RunE: func(cmd *cobra.Command, _ []string) error {
		intentID, _ := cmd.Flags().GetString("intent-id")
		if parsed, err := strconv.ParseInt(intentID, 10, 64); err != nil || parsed <= 0 {
			return fmt.Errorf("--intent-id must be a positive integer")
		}
		clientV2, serverName, ownerAgentID, current, err := contextMutationClient()
		if err != nil {
			return err
		}
		key, err := contextMutationKey("cli-intent-delete")
		if err != nil {
			return err
		}
		response, err := clientV2.DeleteWithBody("/agent-context/intent-actions/"+intentID, map[string]interface{}{
			"expected_context_revision": current.ContextRevision, "idempotency_key": key,
		})
		if err != nil {
			return contextMutationError(err)
		}
		return finishContextMutation(clientV2, serverName, ownerAgentID, response, false)
	},
}

var contextSecurityCmd = &cobra.Command{Use: "security", Short: "Manage owner-confirmed security boundaries"}

var contextSecuritySetCmd = &cobra.Command{
	Use:   "set",
	Short: "Update one or more security-boundary controls",
	RunE: func(cmd *cobra.Command, _ []string) error {
		flagNames := []string{"recurring-publish", "auto-reply-pm", "auto-comment", "show-add-friend"}
		changed := false
		for _, name := range flagNames {
			changed = changed || cmd.Flags().Changed(name)
		}
		if !changed {
			return fmt.Errorf("provide at least one security-boundary flag")
		}
		confirmed, _ := cmd.Flags().GetBool("confirm-elevated")
		for _, name := range []string{"recurring-publish", "auto-reply-pm", "auto-comment"} {
			value, _ := cmd.Flags().GetBool(name)
			if cmd.Flags().Changed(name) && value && !confirmed {
				return fmt.Errorf("enabling --%s requires fresh human confirmation; retry with --confirm-elevated only after explicit approval", name)
			}
		}
		clientV2, serverName, ownerAgentID, current, err := contextMutationClient()
		if err != nil {
			return err
		}
		values := map[string]bool{
			"recurring_publish": current.SecurityBoundary.RecurringPublish,
			"auto_reply_pm":     current.SecurityBoundary.AutoReplyPM,
			"auto_comment":      current.SecurityBoundary.AutoComment,
			"show_add_friend":   current.SecurityBoundary.ShowAddFriend,
		}
		for _, pair := range [][2]string{{"recurring-publish", "recurring_publish"}, {"auto-reply-pm", "auto_reply_pm"}, {"auto-comment", "auto_comment"}, {"show-add-friend", "show_add_friend"}} {
			if cmd.Flags().Changed(pair[0]) {
				values[pair[1]], _ = cmd.Flags().GetBool(pair[0])
			}
		}
		key, err := contextMutationKey("cli-security")
		if err != nil {
			return err
		}
		body := map[string]interface{}{"expected_context_revision": current.ContextRevision, "idempotency_key": key}
		for name, value := range values {
			body[name] = value
		}
		response, err := clientV2.Put("/agent-context/security-boundary", body)
		if err != nil {
			return contextMutationError(err)
		}
		return finishContextMutation(clientV2, serverName, ownerAgentID, response, true)
	},
}

func init() {
	contextGoalSetCmd.Flags().String("text", "", "new network goal")
	contextGoalCmd.AddCommand(contextGoalSetCmd)

	addIntentFlags(contextIntentAddCmd)
	addIntentFlags(contextIntentUpdateCmd)
	contextIntentUpdateCmd.Flags().String("intent-id", "", "intent ID to update")
	contextIntentDeleteCmd.Flags().String("intent-id", "", "intent ID to delete")
	contextIntentCmd.AddCommand(contextIntentListCmd, contextIntentAddCmd, contextIntentUpdateCmd, contextIntentDeleteCmd)

	contextSecuritySetCmd.Flags().Bool("recurring-publish", false, "allow automatic broadcasts")
	contextSecuritySetCmd.Flags().Bool("auto-reply-pm", false, "allow automatic direct-message replies")
	contextSecuritySetCmd.Flags().Bool("auto-comment", false, "allow automatic high-value broadcast replies")
	contextSecuritySetCmd.Flags().Bool("show-add-friend", false, "show the add-friend entry to other Agents")
	contextSecuritySetCmd.Flags().Bool("confirm-elevated", false, "confirm fresh human approval when enabling automatic actions")
	contextSecurityCmd.AddCommand(contextSecuritySetCmd)

	contextV2Cmd.AddCommand(contextGoalCmd, contextIntentCmd, contextSecurityCmd)
}
