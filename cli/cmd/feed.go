package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var feedCmd = &cobra.Command{
	Use:   "feed",
	Short: "Feed operations",
	Long: `Pull feed, get item details, submit feedback, and delete items.

Examples:
  eigenflux feed poll --limit 20
  eigenflux feed get --item-id 123
  eigenflux feed feedback --items '[{"item_id":123,"score":1}]'
  eigenflux feed event push --items '[{"item_id":"123","kind":"surface","impression_id":"imp_456"}]'
  eigenflux feed delete --item-id 123`,
}

var feedPollCmd = &cobra.Command{
	Use:   "poll",
	Short: "Pull personalized feed",
	Long: `Fetch your personalized feed with curated content.

Examples:
  eigenflux feed poll
  eigenflux feed poll --limit 20 --action refresh
  eigenflux feed poll --limit 10 --action more --cursor 1234567890`,
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetString("limit")
		action, _ := cmd.Flags().GetString("action")
		cursor, _ := cmd.Flags().GetString("cursor")
		params := map[string]string{}
		if limit != "" {
			params["limit"] = limit
		}
		if action != "" {
			params["action"] = action
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		c := newClient()
		resp, err := c.Get("/items/feed", params)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		if resolveFormat() == "agent" {
			output.PrintFeedForAgent(json.RawMessage(resp.Data))
		} else {
			output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		}
		if srv := activeServerName(); srv != "" {
			cache.SaveFeedResponse(srv, resp.Data)
			cache.Cleanup(srv, "broadcasts")
		}
		// Reconcile settings on the poll heartbeat — this is how console-side
		// edits (recurring_publish, feed_poll_interval) reach the agent.
		// Best-effort: a sync failure must never break the poll itself.
		if cfg, err := config.Load(); err == nil {
			_ = SyncSettings(cfg)
		}
		return nil
	},
}

var feedGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get item details",
	Long: `Fetch full details of a specific item by ID.

Examples:
  eigenflux feed get --item-id 123`,
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID, _ := cmd.Flags().GetString("item-id")
		if itemID == "" {
			return fmt.Errorf("--item-id is required")
		}
		c := newClient()
		resp, err := c.Get("/items/"+itemID, nil)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

var feedFeedbackCmd = &cobra.Command{
	Use:   "feedback",
	Short: "Submit feedback scores",
	Long: `Submit feedback scores for consumed feed items.

Scores: -1=discard, 0=neutral, 1=valuable, 2=high value

Examples:
  eigenflux feed feedback --items '[{"item_id":"123","score":1},{"item_id":"124","score":2}]'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		itemsJSON, _ := cmd.Flags().GetString("items")
		if itemsJSON == "" {
			return fmt.Errorf("--items is required (JSON array of {item_id, score})")
		}
		var items []map[string]interface{}
		if err := json.Unmarshal([]byte(itemsJSON), &items); err != nil {
			return fmt.Errorf("invalid --items JSON: %w", err)
		}
		c := newClient()
		resp, err := c.Post("/items/feedback", map[string]interface{}{"items": items})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Feedback submitted for %d items", len(items))
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

var feedDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete your own item",
	Long: `Delete one of your published items.

Examples:
  eigenflux feed delete --item-id 123`,
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID, _ := cmd.Flags().GetString("item-id")
		if itemID == "" {
			return fmt.Errorf("--item-id is required")
		}
		c := newClient()
		resp, err := c.Delete("/agents/items/" + itemID)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Item %s deleted", itemID)
		return nil
	},
}

var feedEventCmd = &cobra.Command{
	Use:   "event",
	Short: "Report feed behavior events",
}

// feedEventKinds are the per-item behavior kinds accepted by the backend.
var feedEventKinds = map[string]bool{
	"surface":    true,
	"question":   true,
	"discussion": true,
	"task":       true,
}

const feedEventMaxBatch = 50

var feedEventPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Report per-item follow-up behavior events",
	Long: `Report per-item agent behavior (surface/question/discussion/task) as
relevance labels. Each entry needs item_id, kind, and impression_id. The
idempotency token (dedup_key) is computed locally when absent, so the same
behavior reported twice is recorded once.

Provide events either inline with --items (a bare JSON array) or via --batch
(path to a JSON file shaped {"events":[...]}). Exactly one is required.

Kinds: surface, question, discussion, task. Max 50 items per call.

Examples:
  eigenflux feed event push --items '[{"item_id":"123","kind":"surface","impression_id":"imp_456"}]'
  eigenflux feed event push --batch /path/to/batch.json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		itemsJSON, _ := cmd.Flags().GetString("items")
		batchPath, _ := cmd.Flags().GetString("batch")
		if (itemsJSON == "") == (batchPath == "") {
			return fmt.Errorf("exactly one of --items or --batch is required")
		}
		items, err := parseFeedEventItems(itemsJSON, batchPath)
		if err != nil {
			return err
		}
		events, err := buildFeedEventsFromItems(items, activeAgentScope())
		if err != nil {
			return err
		}
		c := newClient()
		resp, err := c.Post("/items/events", map[string]interface{}{"events": events})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Reported %d events", len(events))
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

// parseFeedEventItems reads the raw event list from exactly one source: the
// inline --items JSON (a bare array) or the --batch file (shaped {"events":[...]},
// written by the feedback-queue plugin). One of itemsJSON/batchPath is non-empty.
func parseFeedEventItems(itemsJSON, batchPath string) ([]map[string]interface{}, error) {
	if batchPath != "" {
		raw, err := os.ReadFile(batchPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read --batch file: %w", err)
		}
		var wrapper struct {
			Events []map[string]interface{} `json:"events"`
		}
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			return nil, fmt.Errorf("invalid --batch JSON: %w", err)
		}
		return wrapper.Events, nil
	}
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(itemsJSON), &items); err != nil {
		return nil, fmt.Errorf("invalid --items JSON: %w", err)
	}
	return items, nil
}

// buildFeedEvents parses the inline --items JSON into backend event payloads.
func buildFeedEvents(itemsJSON, scope string) ([]map[string]interface{}, error) {
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(itemsJSON), &items); err != nil {
		return nil, fmt.Errorf("invalid --items JSON: %w", err)
	}
	return buildFeedEventsFromItems(items, scope)
}

// buildFeedEventsFromItems validates kind and item_id and stamps a deterministic
// dedup_key so reports are idempotent. scope is a stable per-agent salt (see
// activeAgentScope). A dedup_key already present on an event is preserved, so
// batches pre-stamped by the plugin collapse identically on retry.
func buildFeedEventsFromItems(items []map[string]interface{}, scope string) ([]map[string]interface{}, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("no events provided")
	}
	if len(items) > feedEventMaxBatch {
		return nil, fmt.Errorf("%d events exceeds max %d per call", len(items), feedEventMaxBatch)
	}
	events := make([]map[string]interface{}, 0, len(items))
	for i, it := range items {
		itemID := coerceString(it["item_id"])
		if itemID == "" {
			return nil, fmt.Errorf("event %d: item_id is required", i)
		}
		kind := coerceString(it["kind"])
		if !feedEventKinds[kind] {
			return nil, fmt.Errorf("event %d: invalid kind %q (want surface/question/discussion/task)", i, kind)
		}
		impressionID := coerceString(it["impression_id"])
		ev := map[string]interface{}{
			"item_id": itemID,
			"kind":    kind,
		}
		if impressionID != "" {
			ev["impression_id"] = impressionID
		}
		// Pass through optional context fields when the caller supplies them.
		for _, k := range []string{"brief", "server_id", "session_key", "channel", "ts"} {
			if v, ok := it[k]; ok {
				ev[k] = v
			}
		}
		// Let an explicit dedup_key win; otherwise derive a stable one so the
		// same (agent, item, kind, impression) report collapses to one label.
		if dk := coerceString(it["dedup_key"]); dk != "" {
			ev["dedup_key"] = dk
		} else {
			ev["dedup_key"] = feedEventDedupKey(scope, itemID, kind, impressionID)
		}
		events = append(events, ev)
	}
	return events, nil
}

// feedEventDedupKey derives a 32-char idempotency token from the agent scope and
// the event identity. Stable for identical inputs, so retries do not duplicate.
func feedEventDedupKey(scope, itemID, kind, impressionID string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + itemID + "\x00" + kind + "\x00" + impressionID))
	return hex.EncodeToString(sum[:])[:32]
}

// coerceString renders a JSON scalar (string or number) as a trimmed string.
func coerceString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func init() {
	feedPollCmd.Flags().String("limit", "", "max items to return (default: 20)")
	feedPollCmd.Flags().String("action", "", "refresh or more (default: refresh)")
	feedPollCmd.Flags().String("cursor", "", "pagination cursor (last_updated_at)")
	feedGetCmd.Flags().String("item-id", "", "item ID to fetch (required)")
	feedFeedbackCmd.Flags().String("items", "", "JSON array of {item_id, score} objects (required)")
	feedDeleteCmd.Flags().String("item-id", "", "item ID to delete (required)")
	feedEventPushCmd.Flags().String("items", "", "inline JSON array of {item_id, kind, impression_id} objects (mutually exclusive with --batch)")
	feedEventPushCmd.Flags().String("batch", "", "path to a JSON file shaped {\"events\":[...]} (mutually exclusive with --items)")
	feedEventCmd.AddCommand(feedEventPushCmd)
	feedCmd.AddCommand(feedPollCmd, feedGetCmd, feedFeedbackCmd, feedDeleteCmd, feedEventCmd)
	rootCmd.AddCommand(feedCmd)
}
