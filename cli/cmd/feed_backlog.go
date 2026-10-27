package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/controlcontext"
	"cli.eigenflux.ai/internal/feedevent"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

const feedBacklogMaxHolds = 3
const feedBacklogMaxAgeMs int64 = 6 * 60 * 60 * 1000

func holdFeedBacklog(origin, action, mode string, baseline bool, pending []feedevent.PendingItem, holds int) bool {
	return origin == publishOriginHeartbeat && (action == "" || action == "refresh") && mode == "hold" && !baseline && len(pending) > 0 && pending[0].AgeMs < feedBacklogMaxAgeMs && holds < feedBacklogMaxHolds
}
func pendingFeed(server string, state feedevent.Reports, now int64) []feedevent.PendingItem {
	return feedevent.NewLedger(filepath.Join(cache.ServerDataDir(server), "broadcasts"), server).Pending(state, now)
}

// Runtime synchronization has already applied the current owner context. Held
// pages retain their impressions and contracts, but never restore stale context.
func tryHeldFeed(cmd *cobra.Command, c *client.Client, server, agent string, revision int64) (bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return false, err
	}
	mode, _, _ := cfg.GetServerOnlyKV(server, "feed_backlog_mode")
	action, _ := cmd.Flags().GetString("action")
	store := feedReportStore(server)
	state, err := store.Read()
	if err != nil {
		return false, err
	}
	now := time.Now().UnixMilli()
	pending := pendingFeed(server, state, now)
	if !holdFeedBacklog(originFlag, action, mode, state.LastBaseline || revision == 0, pending, state.ConsecutiveHolds) {
		return false, nil
	}
	holds := 0
	if err := store.Update(now, func(r *feedevent.Reports) {
		pending = pendingFeed(server, *r, now)
		if holdFeedBacklog(originFlag, action, mode, r.LastBaseline || revision == 0, pending, r.ConsecutiveHolds) {
			r.ConsecutiveHolds++
			holds = r.ConsecutiveHolds
			r.Count(now, map[string]int64{"backlog_holds": 1})
		}
	}); err != nil {
		return false, err
	}
	if holds == 0 {
		return false, nil
	}
	response, err := c.Get("/notifications/pending", nil)
	if err != nil {
		return true, err
	}
	if response.Code != 0 {
		return true, fmt.Errorf("%s", response.Msg)
	}
	var notifications struct {
		Notifications []json.RawMessage `json:"notifications"`
	}
	if err := json.Unmarshal(response.Data, &notifications); err != nil {
		return true, err
	}
	snapshot, err := controlcontext.Load(server, agent)
	if err != nil {
		return true, err
	}
	payload, err := heldFeedPayload(pending, notifications.Notifications, snapshot.Context, revision, holds)
	if err != nil {
		return true, err
	}
	return true, renderFeedV2(cmd, payload)
}
func heldFeedPayload(pending []feedevent.PendingItem, notifications []json.RawMessage, context json.RawMessage, revision int64, holds int) (json.RawMessage, error) {
	if len(pending) == 0 {
		return nil, fmt.Errorf("empty backlog")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(pending[0].Entry.Envelope, &envelope); err != nil {
		return nil, err
	}
	items := make([]map[string]json.RawMessage, 0, len(pending))
	for _, entry := range pending {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(entry.Entry.Item, &item); err != nil {
			return nil, err
		}
		item["impression_id"], _ = json.Marshal(entry.ImpressionID)
		item["already_surfaced"], _ = json.Marshal(entry.AlreadySurfaced)
		items = append(items, item)
	}
	envelope["items"], _ = json.Marshal(items)
	if notifications == nil {
		notifications = []json.RawMessage{}
	}
	envelope["notifications"], _ = json.Marshal(notifications)
	envelope["backlog"], _ = json.Marshal(map[string]any{"held": true, "pending_items": len(pending), "oldest_pending_age_ms": pending[0].AgeMs, "consecutive_holds": holds})
	envelope["personalization"], _ = json.Marshal(map[string]any{"mode": "intent_aligned", "context_revision": revision, "context_delivery": "full"})
	envelope["control_context_snapshot"] = context
	// A backlog may combine pages. Attribution is always the per-item impression.
	delete(envelope, "impression_id")
	delete(envelope, "discovery")
	delete(envelope, "agent_card_updates")
	envelope["has_more"] = json.RawMessage("false")
	envelope["next_cursor"] = json.RawMessage("null")
	return json.Marshal(envelope)
}
func recordFeedPull(server string, data json.RawMessage) error {
	now := time.Now().UnixMilli()
	var feed struct {
		ImpressionID    string `json:"impression_id"`
		Personalization struct {
			Mode string `json:"mode"`
		} `json:"personalization"`
		Items []struct {
			ItemID string `json:"item_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &feed); err != nil {
		return err
	}
	store := feedReportStore(server)
	return store.Update(now, func(r *feedevent.Reports) {
		counts := map[string]int64{"pulls": 1, "delivered_items": int64(len(feed.Items))}
		if len(pendingFeed(server, *r, now)) > 0 {
			counts["backlog_releases"] = 1
		}
		r.ConsecutiveHolds = 0
		r.LastBaseline = feed.Personalization.Mode == "baseline"
		for _, item := range feed.Items {
			key := feed.ImpressionID + "\x00" + item.ItemID
			entry := r.Entries[key]
			entry.ItemID = item.ItemID
			entry.ImpressionID = feed.ImpressionID
			entry.ServedAt = now
			entry.Baseline = r.LastBaseline
			r.Entries[key] = entry
		}
		r.Count(now, counts)
	})
}

type feedStatus struct {
	Items       []feedevent.PendingItem `json:"items"`
	Pending     int                     `json:"pending"`
	OldestAgeMs int64                   `json:"oldest_age_ms"`
	HeldCycles  int                     `json:"held_cycles"`
}

func buildFeedStatus(pending []feedevent.PendingItem, holds int) feedStatus {
	status := feedStatus{Items: pending, Pending: len(pending), HeldCycles: holds}
	if len(pending) > 0 {
		status.OldestAgeMs = pending[0].AgeMs
	}
	if len(status.Items) > 20 {
		status.Items = status.Items[:20]
	}
	return status
}

var feedStatusCmd = &cobra.Command{Use: "status", Short: "List unscored Feed exposures from the last 48 hours", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	server := activeServerName()
	if server == "" {
		return fmt.Errorf("no active server")
	}
	now := time.Now().UnixMilli()
	store := feedReportStore(server)
	state, err := store.Read()
	if err != nil {
		return err
	}
	status := buildFeedStatus(pendingFeed(server, state, now), state.ConsecutiveHolds)
	if err := store.Count(now, map[string]int64{"status_runs": 1}); err != nil {
		return err
	}
	if resolveFormat() == "agent" {
		for _, item := range status.Items {
			fmt.Fprintf(cmd.OutOrStdout(), "item_id=%s impression_id=%s age_ms=%d already_surfaced=%t title=%q\n", item.ItemID, item.ImpressionID, item.AgeMs, item.AlreadySurfaced, item.Title)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "pending=%d oldest_age=%dms held_cycles=%d\n", status.Pending, status.OldestAgeMs, status.HeldCycles)
	} else {
		output.PrintData(status, resolveFormat())
	}
	return nil
}}

func init() { feedCmd.AddCommand(feedStatusCmd) }
