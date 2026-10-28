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
		_, _ = fmt.Fprintf(os.Stderr, "EigenFlux feed report ledger not updated: %v\n", err)
	}
}

func recordFeedPull(server string, data json.RawMessage) error {
	now := time.Now().UnixMilli()
	var recordErr error
	err := feedReportStore(server).Update(now, func(r *feedevent.Reports) { _, recordErr = recordFeedPage(r, data, now, 0) })
	if err != nil {
		return err
	}
	return recordErr
}

func recordFeedPage(r *feedevent.Reports, data json.RawMessage, now int64, batch int) ([]feedevent.Entry, error) {
	var feed struct {
		ImpressionID    string `json:"impression_id"`
		Personalization struct {
			Mode            string `json:"mode"`
			ContextRevision int64  `json:"context_revision"`
		} `json:"personalization"`
		Items []struct {
			ItemID string `json:"item_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &feed); err != nil {
		return nil, err
	}
	baseline := feed.Personalization.Mode == "baseline"
	selected := []feedevent.Entry{}
	for position, item := range feed.Items {
		key := feed.ImpressionID + "\x00" + item.ItemID
		entry := r.Entries[key]
		entry.ItemID = item.ItemID
		entry.ImpressionID = feed.ImpressionID
		if entry.ServedAt == 0 {
			entry.ServedAt = now
			unhanded := int64(0)
			entry.HandedAt = &unhanded
		}
		entry.Baseline = baseline
		entry.Position = position
		entry.ContextRevision = feed.Personalization.ContextRevision
		if batch == 0 || baseline || position < batch {
			entry.Hand(now)
			selected = append(selected, feedevent.Entry{ItemID: item.ItemID, ImpressionID: feed.ImpressionID, Position: position})
		}
		r.Entries[key] = entry
	}
	r.LastFetchAt, r.LocalPolls = now, 0
	r.Count(now, map[string]int64{"pulls": 1, "delivered_items": int64(len(feed.Items))})
	if batch > 0 && !baseline {
		r.Count(now, map[string]int64{"batches_handed": 1})
	}
	return selected, nil
}

type feedStatus struct {
	Items             []feedevent.PendingItem `json:"items"`
	Pending           int                     `json:"pending"`
	OldestAgeMs       int64                   `json:"oldest_age_ms"`
	Open              int                     `json:"open"`
	Backlog           int                     `json:"backlog"`
	SurfaceUnreported []feedevent.PendingItem `json:"surface_unreported"`
	Health            map[string]int64        `json:"health"`
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
	ledger := feedLedger(server)
	var status feedStatus
	err := store.Update(now, func(state *feedevent.Reports) {
		expireFeedBacklog(ledger, state, now)
		status = buildFeedStatus(ledger.Pending(*state, now))
		open, backlog := batchCandidates(ledger, *state, now)
		status.Open, status.Backlog = len(open), len(backlog)
		status.SurfaceUnreported = []feedevent.PendingItem{}
		for _, e := range ledger.ReportEntries(*state) {
			r := state.Exposure(e)
			if !e.Feed || r.Baseline || r.HandedTime() == 0 || r.ScoredAt <= now-feedSurfaceReminderWindow.Milliseconds() || r.ScoredAt > now ||
				(r.Score != 1 && r.Score != 2) || state.Surfaced(e.ItemID) || r.SurfaceRemindedAt > 0 {
				continue
			}
			status.SurfaceUnreported = append(status.SurfaceUnreported, feedevent.PendingItem{ItemID: e.ItemID, ImpressionID: e.ImpressionID, Title: e.Title, AgeMs: now - r.ScoredAt})
			r.SurfaceRemindedAt = now
			state.Entries[r.ImpressionID+"\x00"+r.ItemID] = r
		}
		state.Count(now, map[string]int64{"status_runs": 1, "surface_reminders": int64(len(status.SurfaceUnreported))})
		status.Health = state.HealthCounts(now)
	})
	if err != nil {
		return err
	}
	if resolveFormat() == "agent" {
		for _, item := range status.Items {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "item_id=%s impression_id=%s age_ms=%d already_surfaced=%t title=%q\n", item.ItemID, item.ImpressionID, item.AgeMs, item.AlreadySurfaced, item.Title)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "pending=%d oldest_age=%dms open=%d backlog=%d\n", status.Pending, status.OldestAgeMs, status.Open, status.Backlog)
		for _, item := range status.SurfaceUnreported {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "surface_unreported item_id=%s impression_id=%s title=%q\n", item.ItemID, item.ImpressionID, item.Title)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "surface_unreported=%d (report only items actually shown)\n", len(status.SurfaceUnreported))
	} else {
		output.PrintData(status, resolveFormat())
	}
	return nil
}}

func init() { feedCmd.AddCommand(feedStatusCmd) }
