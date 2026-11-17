package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/feedevent"
	"github.com/spf13/cobra"
)

type feedDeferItem struct {
	ItemID       string `json:"item_id"`
	ImpressionID string `json:"impression_id"`
}

func deferFeedItems(store *feedevent.ReportStore, ledger *feedevent.Ledger, cycle string, items []feedDeferItem, now int64) (int, error) {
	var judgmentErr error
	count := 0
	err := store.Update(now, func(state *feedevent.Reports) {
		c, exists := state.Cycles[cycle]
		if !exists {
			judgmentErr = fmt.Errorf("Feed cycle %q is unavailable", cycle)
			return
		}
		entries := make(map[string]feedevent.Report, len(items))
		for i, item := range items {
			if item.ItemID == "" || item.ImpressionID == "" || strings.ContainsRune(item.ItemID, 0) || strings.ContainsRune(item.ImpressionID, 0) {
				judgmentErr = fmt.Errorf("item %d: item_id and impression_id are required", i)
				return
			}
			exposure, status := ledger.LookupImpression(item.ItemID, item.ImpressionID, now)
			r := state.Exposure(exposure)
			if status != feedevent.StatusHit || !exposure.Feed || r.Baseline || !r.Batched || r.CycleID != cycle ||
				r.HandedTime() <= 0 || r.ScoredAt != 0 || r.Delivery != "" || state.Surfaced(item.ItemID) {
				judgmentErr = fmt.Errorf("item %d: exposure is not an unscored item handed in Feed cycle %q", i, cycle)
				return
			}
			if r.BacklogExpiredAt > 0 || r.ServedAt <= now-feedBacklogAge.Milliseconds() || r.ServedAt > now {
				judgmentErr = fmt.Errorf("item %d: Feed exposure has expired or is unavailable", i)
				return
			}
			if r.DeferredAt == 0 && !c.Active(now) {
				judgmentErr = fmt.Errorf("Feed cycle %q is closed or expired", cycle)
				return
			}
			entries[item.ImpressionID+"\x00"+item.ItemID] = r
		}
		var newlyDeferred int64
		for key, r := range entries {
			if r.DeferredAt == 0 {
				r.DeferredAt = now
				state.Entries[key] = r
				newlyDeferred++
			}
		}
		if newlyDeferred > 0 {
			state.Count(now, map[string]int64{"deferred_items": newlyDeferred})
		}
		count = len(entries)
	})
	if err != nil {
		return 0, err
	}
	return count, judgmentErr
}

var feedDeferCmd = &cobra.Command{
	Use:   "defer",
	Short: "Leave unscored items for a later Feed cycle",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		server := activeServerName()
		if server == "" {
			return fmt.Errorf("no active server")
		}
		credentials, err := auth.LoadV2Credentials(server)
		if err != nil {
			return err
		}
		if credentials.AgentID == "" {
			return fmt.Errorf("authenticated Agent ID is unavailable for server %q", server)
		}
		cycle, _ := cmd.Flags().GetString("cycle")
		if cycle == "" {
			return fmt.Errorf("--cycle is required")
		}
		raw, _ := cmd.Flags().GetString("items")
		var items []feedDeferItem
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return fmt.Errorf("invalid --items JSON: %w", err)
		}
		if len(items) == 0 || len(items) > feedevent.MaxBatch {
			return fmt.Errorf("provide between 1 and %d items to defer", feedevent.MaxBatch)
		}
		count, err := deferFeedItems(feedReportStore(server), feedLedger(server), cycle, items, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]int{"deferred_count": count})
	},
}

func init() {
	feedDeferCmd.Flags().String("cycle", "", "Feed cycle that handed the items (required)")
	feedDeferCmd.Flags().String("items", "", "JSON array of {item_id, impression_id} from that cycle (required)")
	feedCmd.AddCommand(feedDeferCmd)
}
