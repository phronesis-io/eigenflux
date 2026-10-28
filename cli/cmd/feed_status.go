package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/feedevent"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

func pendingFeed(server string, state feedevent.Reports, now int64) []feedevent.PendingItem {
	return feedevent.NewLedger(filepath.Join(cache.ServerDataDir(server), "broadcasts"), server).Pending(state, now)
}

// warnReportLedger reports a local ledger failure without failing the command:
// the ledger is diagnostic, while Feed delivery and feedback delivery are not.
func warnReportLedger(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "EigenFlux feed report ledger not updated: %v\n", err)
	}
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
	baseline := feed.Personalization.Mode == "baseline"
	return feedReportStore(server).Update(now, func(r *feedevent.Reports) {
		for _, item := range feed.Items {
			key := feed.ImpressionID + "\x00" + item.ItemID
			entry := r.Entries[key]
			entry.ItemID = item.ItemID
			entry.ImpressionID = feed.ImpressionID
			entry.ServedAt = now
			entry.Baseline = baseline
			r.Entries[key] = entry
		}
		r.Count(now, map[string]int64{"pulls": 1, "delivered_items": int64(len(feed.Items))})
	})
}

type feedStatus struct {
	Items       []feedevent.PendingItem `json:"items"`
	Pending     int                     `json:"pending"`
	OldestAgeMs int64                   `json:"oldest_age_ms"`
}

func buildFeedStatus(pending []feedevent.PendingItem) feedStatus {
	status := feedStatus{Items: pending, Pending: len(pending)}
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
	status := buildFeedStatus(pendingFeed(server, state, now))
	warnReportLedger(store.Count(now, map[string]int64{"status_runs": 1}))
	if resolveFormat() == "agent" {
		for _, item := range status.Items {
			fmt.Fprintf(cmd.OutOrStdout(), "item_id=%s impression_id=%s age_ms=%d already_surfaced=%t title=%q\n", item.ItemID, item.ImpressionID, item.AgeMs, item.AlreadySurfaced, item.Title)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "pending=%d oldest_age=%dms\n", status.Pending, status.OldestAgeMs)
	} else {
		output.PrintData(status, resolveFormat())
	}
	return nil
}}

func init() { feedCmd.AddCommand(feedStatusCmd) }
