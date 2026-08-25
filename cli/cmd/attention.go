package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/controlcontext"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

const (
	attentionSchemaVersion   = "agent_attention.v1"
	attentionPublishEndpoint = "/agent-attention-items:publish"
	attentionBodyLimit       = 32 << 10
	attentionItemLimit       = 10
	attentionActionLimit     = 5
	attentionCustomFlagLimit = 20
	attentionMaxLifetimeMS   = int64(90 * 24 * 60 * 60 * 1000)
)

var attentionBatchIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
var attentionActionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var attentionCategories = map[string]map[string]struct{}{
	"participation": {
		"action_recommendation": {},
		"goal_calibration":      {},
		"intent_update":         {},
		"other_decision":        {},
	},
	"focus": {
		"important_signal":      {},
		"opportunity":           {},
		"relationship_created":  {},
		"relationship_feedback": {},
		"watch_update":          {},
		"other_attention":       {},
	},
}

var attentionPresetFlags = map[string]map[string]struct{}{
	"participation": {
		"approve_first_contact": {},
		"observe_first":         {},
		"apply_goal_update":     {},
		"keep_goal":             {},
		"apply_intent_update":   {},
		"keep_intent":           {},
		"follow_up":             {},
		"not_interested":        {},
	},
	"focus": {
		"open_source":         {},
		"ask_agent_contact":   {},
		"add_watch":           {},
		"ask_agent_summarize": {},
		"draft_broadcast":     {},
		"follow_up":           {},
		"not_interested":      {},
	},
}

var attentionSourceTypes = map[string]struct{}{
	"broadcast":       {},
	"broadcast_reply": {},
	"friend_request":  {},
	"relation":        {},
	"private_message": {},
	"context":         {},
	"activity":        {},
}

type attentionPublishRequest struct {
	SchemaVersion  string          `json:"schema_version"`
	IdempotencyKey string          `json:"idempotency_key"`
	Items          []attentionItem `json:"items"`
}

type attentionItem struct {
	ClientItemID   string               `json:"client_item_id"`
	Surface        string               `json:"surface"`
	Category       string               `json:"category"`
	Language       string               `json:"language"`
	Title          string               `json:"title"`
	Body           string               `json:"body"`
	Recommendation string               `json:"recommendation,omitempty"`
	SourceRef      *attentionSourceRef  `json:"source_ref,omitempty"`
	ContextRef     *attentionContextRef `json:"context_ref,omitempty"`
	Actions        []attentionAction    `json:"actions"`
	GeneratedAt    int64                `json:"generated_at"`
	ExpiresAt      int64                `json:"expires_at"`
}

type attentionSourceRef struct {
	Type     string  `json:"type"`
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id,omitempty"`
}

type attentionContextRef struct {
	Operation           string  `json:"operation,omitempty"`
	ContextRevision     int64   `json:"context_revision"`
	NetworkGoalRevision int64   `json:"network_goal_revision,omitempty"`
	IntentID            *string `json:"intent_id,omitempty"`
}

type attentionAction struct {
	ActionKey  string `json:"action_key"`
	Kind       string `json:"kind"`
	Flag       string `json:"flag"`
	Appearance string `json:"appearance"`
}

var attentionCmd = &cobra.Command{
	Use:   "attention",
	Short: "Publish Agent-generated items that need human attention",
}

var attentionPublishCmd = &cobra.Command{
	Use:   "publish --stdin",
	Short: "Validate and publish one Agent Attention batch from standard input",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		fromStdin, _ := cmd.Flags().GetBool("stdin")
		if !fromStdin {
			return fmt.Errorf("--stdin is required")
		}
		request, err := readAttentionPublishRequest(cmd.InOrStdin())
		if err != nil {
			return err
		}

		clientV2, server, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		credentials, err := auth.LoadV2Credentials(server.Name)
		if err != nil {
			return err
		}
		if err := rejectFullLocalIntentAdds(server.Name, credentials.AgentID, request); err != nil {
			return err
		}

		response, err := clientV2.Post(attentionPublishEndpoint, request)
		if err != nil {
			return err
		}
		output.PrintData(response.Data, resolveFormat())
		return nil
	},
}

func readAttentionPublishRequest(reader io.Reader) (attentionPublishRequest, error) {
	data, err := io.ReadAll(io.LimitReader(reader, attentionBodyLimit+1))
	if err != nil {
		return attentionPublishRequest{}, fmt.Errorf("read attention payload: %w", err)
	}
	if len(data) == 0 {
		return attentionPublishRequest{}, fmt.Errorf("attention payload is empty")
	}
	if len(data) > attentionBodyLimit {
		return attentionPublishRequest{}, fmt.Errorf("attention payload must not exceed 32 KiB")
	}
	if !utf8.Valid(data) {
		return attentionPublishRequest{}, fmt.Errorf("attention payload must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request attentionPublishRequest
	if err := decoder.Decode(&request); err != nil {
		return attentionPublishRequest{}, fmt.Errorf("attention payload must be a typed JSON object: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return attentionPublishRequest{}, err
	}
	if err := validateAttentionPublishRequest(request); err != nil {
		return attentionPublishRequest{}, err
	}
	return request, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("attention payload must contain exactly one JSON object")
		}
		return fmt.Errorf("invalid trailing data in attention payload: %w", err)
	}
	return nil
}

func validateAttentionPublishRequest(request attentionPublishRequest) error {
	if request.SchemaVersion != attentionSchemaVersion {
		return fmt.Errorf("schema_version must be %q", attentionSchemaVersion)
	}
	if !attentionBatchIDPattern.MatchString(request.IdempotencyKey) {
		return fmt.Errorf("idempotency_key must be a stable identifier of 8-128 ASCII letters, digits, underscores, or hyphens")
	}
	if len(request.Items) < 1 || len(request.Items) > attentionItemLimit {
		return fmt.Errorf("items must contain between 1 and %d entries", attentionItemLimit)
	}
	seenItems := make(map[string]struct{}, len(request.Items))
	for index, item := range request.Items {
		if _, exists := seenItems[item.ClientItemID]; exists {
			return fmt.Errorf("items[%d].client_item_id is duplicated in this batch", index)
		}
		seenItems[item.ClientItemID] = struct{}{}
		if err := validateAttentionItem(item); err != nil {
			return fmt.Errorf("items[%d]: %w", index, err)
		}
	}
	return nil
}

func validateAttentionItem(item attentionItem) error {
	if !attentionBatchIDPattern.MatchString(item.ClientItemID) {
		return fmt.Errorf("client_item_id must be a stable identifier of 8-128 ASCII letters, digits, underscores, or hyphens")
	}
	categories, ok := attentionCategories[item.Surface]
	if !ok {
		return fmt.Errorf("surface must be participation or focus")
	}
	if _, ok := categories[item.Category]; !ok {
		return fmt.Errorf("category %q is not valid for surface %q", item.Category, item.Surface)
	}
	if item.Language != "zh-CN" && item.Language != "en" {
		return fmt.Errorf("language must be zh-CN or en")
	}
	if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Body) == "" {
		return fmt.Errorf("title and body are required")
	}
	if utf8.RuneCountInString(item.Title) > 120 || utf8.RuneCountInString(item.Body) > 2000 ||
		utf8.RuneCountInString(item.Recommendation) > 1000 {
		return fmt.Errorf("title, body, and recommendation exceed their 120, 2000, or 1000 character limit")
	}
	if item.Surface == "participation" && strings.TrimSpace(item.Recommendation) == "" {
		return fmt.Errorf("recommendation is required for participation items")
	}
	if item.GeneratedAt < 1_000_000_000_000 || item.ExpiresAt <= item.GeneratedAt ||
		item.ExpiresAt > item.GeneratedAt+attentionMaxLifetimeMS {
		return fmt.Errorf("generated_at and expires_at must be Unix milliseconds with a positive lifetime no longer than 90 days")
	}
	if item.SourceRef != nil {
		if _, ok := attentionSourceTypes[item.SourceRef.Type]; !ok {
			return fmt.Errorf("source_ref.type is not supported")
		}
		if err := validateAttentionReferenceID("source_ref.id", item.SourceRef.ID); err != nil {
			return err
		}
		if item.SourceRef.ParentID != nil {
			if err := validateAttentionReferenceID("source_ref.parent_id", *item.SourceRef.ParentID); err != nil {
				return err
			}
		}
		if item.SourceRef.Type == "broadcast_reply" && item.SourceRef.ParentID == nil {
			return fmt.Errorf("broadcast_reply source_ref requires parent_id")
		}
	}
	if attentionCategoryRequiresSource(item.Category) && item.SourceRef == nil {
		return fmt.Errorf("source_ref is required for category %q", item.Category)
	}
	if err := validateAttentionContextRef(item); err != nil {
		return err
	}
	if len(item.Actions) < 1 || len(item.Actions) > attentionActionLimit {
		return fmt.Errorf("actions must contain between 1 and %d entries", attentionActionLimit)
	}
	seenActions := make(map[string]struct{}, len(item.Actions))
	primaryCount := 0
	for index, action := range item.Actions {
		if _, exists := seenActions[action.ActionKey]; exists {
			return fmt.Errorf("actions[%d].action_key is duplicated in this item", index)
		}
		seenActions[action.ActionKey] = struct{}{}
		if err := validateAttentionAction(item.Surface, action); err != nil {
			return fmt.Errorf("actions[%d]: %w", index, err)
		}
		if action.Appearance == "primary" {
			primaryCount++
		}
		if action.Flag == "open_source" && item.SourceRef == nil {
			return fmt.Errorf("actions[%d]: open_source requires source_ref", index)
		}
	}
	if primaryCount > 1 {
		return fmt.Errorf("actions may contain at most one primary appearance")
	}
	return nil
}

func validateAttentionContextRef(item attentionItem) error {
	calibration := item.Category == "goal_calibration" || item.Category == "intent_update"
	if item.ContextRef == nil {
		if calibration {
			return fmt.Errorf("context_ref is required for %s", item.Category)
		}
		return nil
	}
	ref := item.ContextRef
	if ref.ContextRevision <= 0 {
		return fmt.Errorf("context_ref.context_revision must be positive")
	}
	if ref.Operation != "" && ref.Operation != "add" && ref.Operation != "update" {
		return fmt.Errorf("context_ref.operation must be add or update")
	}
	switch item.Category {
	case "goal_calibration":
		if (ref.Operation != "" && ref.Operation != "update") || ref.NetworkGoalRevision <= 0 || ref.IntentID != nil {
			return fmt.Errorf("goal_calibration requires a positive network_goal_revision, no intent_id, and operation=update when operation is present")
		}
	case "intent_update":
		if ref.Operation == "" {
			return fmt.Errorf("intent_update requires context_ref.operation=add or update")
		}
		if ref.Operation == "add" && ref.IntentID != nil {
			return fmt.Errorf("intent_update operation=add must not include intent_id")
		}
		if ref.Operation == "update" && (ref.IntentID == nil || !isPositiveDecimalID(*ref.IntentID)) {
			return fmt.Errorf("intent_update operation=update requires a positive intent_id")
		}
	}
	return nil
}

func validateAttentionAction(surface string, action attentionAction) error {
	if !attentionActionIDPattern.MatchString(action.ActionKey) {
		return fmt.Errorf("action_key must contain 1-128 ASCII letters, digits, underscores, or hyphens")
	}
	if action.Appearance != "primary" && action.Appearance != "secondary" {
		return fmt.Errorf("appearance must be primary or secondary")
	}
	switch action.Kind {
	case "preset":
		if _, ok := attentionPresetFlags[surface][action.Flag]; !ok {
			return fmt.Errorf("preset flag %q is not valid for surface %q", action.Flag, surface)
		}
	case "custom":
		if err := validateCustomAttentionFlag(action.Flag); err != nil {
			return err
		}
	default:
		return fmt.Errorf("kind must be preset or custom")
	}
	return nil
}

func validateCustomAttentionFlag(flag string) error {
	if !utf8.ValidString(flag) || strings.TrimSpace(flag) != flag || flag == "" || len([]byte(flag)) > attentionCustomFlagLimit {
		return fmt.Errorf("custom flag must contain 1-%d UTF-8 bytes without surrounding whitespace", attentionCustomFlagLimit)
	}
	if strings.ContainsAny(flag, "\r\n<>") {
		return fmt.Errorf("custom flag must not contain newlines or HTML")
	}
	for _, value := range flag {
		if unicode.IsControl(value) {
			return fmt.Errorf("custom flag must not contain control characters")
		}
	}
	return nil
}

func validateAttentionReferenceID(field, value string) error {
	if strings.TrimSpace(value) != value || value == "" || len([]byte(value)) > 256 || !isPositiveDecimalID(value) {
		return fmt.Errorf("%s must be a positive decimal identifier", field)
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return fmt.Errorf("%s must not contain control characters", field)
		}
	}
	return nil
}

func attentionCategoryRequiresSource(category string) bool {
	switch category {
	case "action_recommendation", "important_signal", "opportunity", "relationship_created", "relationship_feedback":
		return true
	default:
		return false
	}
}

func isPositiveDecimalID(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0
}

func rejectFullLocalIntentAdds(serverName, ownerAgentID string, request attentionPublishRequest) error {
	containsIntentAdd := false
	for _, item := range request.Items {
		if item.Category == "intent_update" && item.ContextRef != nil && item.ContextRef.Operation == "add" {
			containsIntentAdd = true
			break
		}
	}
	if !containsIntentAdd {
		return nil
	}
	snapshot, err := controlcontext.Load(serverName, ownerAgentID)
	if err != nil {
		return nil
	}
	var context struct {
		IntentActions []struct {
			Status string `json:"status"`
		} `json:"intent_actions"`
	}
	if json.Unmarshal(snapshot.Context, &context) != nil {
		return nil
	}
	active := 0
	for _, intent := range context.IntentActions {
		if intent.Status == "" || intent.Status == "active" {
			active++
		}
	}
	if active >= 10 {
		return fmt.Errorf("active intent limit reached; intent_update operation=add is not allowed")
	}
	return nil
}

func init() {
	attentionPublishCmd.Flags().Bool("stdin", false, "read one agent_attention.v1 JSON batch from standard input")
	attentionCmd.AddCommand(attentionPublishCmd)
	rootCmd.AddCommand(attentionCmd)
}
