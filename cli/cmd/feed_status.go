package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"cli.eigenflux.ai/internal/feedevent"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

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
	err := feedReportStore(server).Update(now, func(r *feedevent.Reports) { _, recordErr = recordFeedPage(r, data, now, 0, false) })
	if err != nil {
		return err
	}
	return recordErr
}

func recordFeedPage(r *feedevent.Reports, data json.RawMessage, now int64, batch int, deferred bool) ([]feedevent.Entry, error) {
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
			if batch > 0 && !baseline {
				unhanded := int64(0)
				entry.HandedAt = &unhanded
			}
		}
		if batch == 0 || baseline {
			entry.HandedAt = nil
			entry.HandCount = 0
			entry.Batched = false
		}
		entry.Baseline = baseline
		entry.Position = position
		entry.ContextRevision = feed.Personalization.ContextRevision
		if batch > 0 && !baseline && (position < batch || deferred) {
			if !deferred {
				entry.Hand(now)
			}
			selected = append(selected, feedevent.Entry{ItemID: item.ItemID, ImpressionID: feed.ImpressionID, Position: position})
		}
		r.Entries[key] = entry
	}
	r.LastFetchAt, r.LocalPolls = now, 0
	r.Count(now, map[string]int64{"pulls": 1, "delivered_items": int64(len(feed.Items))})
	if batch > 0 && !baseline && !deferred {
		r.Count(now, map[string]int64{"batches_handed": 1})
	}
	return selected, nil
}

type feedStatus struct {
	Items       []feedevent.PendingItem `json:"items"`
	Pending     int                     `json:"pending"`
	Deferred    int                     `json:"deferred"`
	OldestAgeMs int64                   `json:"oldest_age_ms"`
	Open        int                     `json:"open"`
	Backlog     int                     `json:"backlog"`
	Health      map[string]int64        `json:"health"`
}

type feedSurfaceStatus struct {
	SurfaceUnreported []feedevent.PendingItem `json:"surface_unreported"`
}

func buildFeedStatus(pending []feedevent.PendingItem) feedStatus {
	status := feedStatus{Items: pending, Pending: len(pending)}
	for _, item := range pending {
		if item.Deferred {
			status.Deferred++
		}
	}
	if len(pending) > 0 {
		status.OldestAgeMs = pending[0].AgeMs
	}
	if len(status.Items) > 20 {
		status.Items = status.Items[:20]
	}
	return status
}

func surfaceUnreported(ledger *feedevent.Ledger, state feedevent.Reports, now int64, cycle string) []feedevent.PendingItem {
	items := []feedevent.PendingItem{}
	for _, e := range ledger.ReportEntries(state) {
		r := state.Exposure(e)
		if cycle != "" && r.CycleID != cycle {
			continue
		}
		if !e.Feed || r.Baseline || r.HandedTime() == 0 || r.ScoredAt <= now-feedSurfaceReminderWindow.Milliseconds() || r.ScoredAt > now ||
			(r.Score != 1 && r.Score != 2) || state.Surfaced(e.ItemID) || r.SurfaceRemindedAt > 0 {
			continue
		}
		items = append(items, feedevent.PendingItem{ItemID: e.ItemID, ImpressionID: e.ImpressionID, Title: e.Title, AgeMs: now - r.ScoredAt})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ImpressionID != items[j].ImpressionID {
			return items[i].ImpressionID < items[j].ImpressionID
		}
		return items[i].ItemID < items[j].ItemID
	})
	return items
}

// The post-push check claims reminders under the report lock so each exposure is
// listed once. A busy ledger still lists them, without claiming.
func claimSurfaceReminders(store *feedevent.ReportStore, ledger *feedevent.Ledger, now int64, cycle string) ([]feedevent.PendingItem, error) {
	var items []feedevent.PendingItem
	var cycleErr error
	err := store.Update(now, func(state *feedevent.Reports) {
		if cycle != "" {
			if _, exists := state.Cycles[cycle]; !exists {
				cycleErr = fmt.Errorf("Feed cycle %q is unavailable", cycle)
				return
			}
		}
		items = surfaceUnreported(ledger, *state, now, cycle)
		for _, item := range items {
			key := item.ImpressionID + "\x00" + item.ItemID
			r := state.Entries[key]
			r.SurfaceRemindedAt = now
			state.Entries[key] = r
		}
		state.Count(now, map[string]int64{"surface_reminders": int64(len(items))})
		if cycle != "" {
			c := state.Cycles[cycle]
			if c.ClosedAt == 0 {
				c.ClosedAt = now
			}
			state.Cycles[cycle] = c
		}
	})
	if err == nil {
		return items, cycleErr
	}
	if cycle != "" {
		return nil, err
	}
	warnReportLedger(err)
	state, readErr := store.Read()
	if readErr != nil {
		return nil, readErr
	}
	return surfaceUnreported(ledger, state, now, cycle), nil
}

var feedStatusCmd = &cobra.Command{Use: "status", Short: "List unscored Feed exposures from the last 48 hours", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	server := activeServerName()
	if server == "" {
		return fmt.Errorf("no active server")
	}
	now := time.Now().UnixMilli()
	store := feedReportStore(server)
	ledger := feedLedger(server)
	cycle, _ := cmd.Flags().GetString("cycle")
	if cycle != "" {
		state, err := store.Read()
		if err != nil {
			return err
		}
		if _, exists := state.Cycles[cycle]; !exists {
			return fmt.Errorf("Feed cycle %q is unavailable", cycle)
		}
	}
	if afterPush, _ := cmd.Flags().GetBool("after-push"); afterPush {
		items, err := claimSurfaceReminders(store, ledger, now, cycle)
		if err != nil {
			return err
		}
		if resolveFormat() != "agent" {
			output.PrintData(feedSurfaceStatus{SurfaceUnreported: items}, resolveFormat())
			return nil
		}
		for _, item := range items {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "surface_unreported item_id=%s impression_id=%s title=%q\n", item.ItemID, item.ImpressionID, item.Title)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "surface_unreported=%d (report only items actually shown)\n", len(items))
		return nil
	}
	state, err := store.Read()
	if err != nil {
		return err
	}
	warnReportLedger(store.Update(now, func(current *feedevent.Reports) {
		expireFeedBacklog(ledger, current, now)
		state = *current
		current.Count(now, map[string]int64{"status_runs": 1})
	}))
	pending := ledger.Pending(state, now)
	if cycle != "" {
		filtered := pending[:0]
		for _, item := range pending {
			if state.Entries[item.ImpressionID+"\x00"+item.ItemID].CycleID == cycle {
				filtered = append(filtered, item)
			}
		}
		pending = filtered
	}
	status := buildFeedStatus(pending)
	open, backlog := cycleBatchCandidates(ledger, state, now, cycle, cycle != "")
	status.Open, status.Backlog = len(open), len(backlog)
	status.Health = state.HealthCounts(now)
	if resolveFormat() == "agent" {
		for _, item := range status.Items {
			deferred := ""
			if item.Deferred {
				deferred = " deferred=true"
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "item_id=%s impression_id=%s age_ms=%d already_surfaced=%t title=%q%s\n", item.ItemID, item.ImpressionID, item.AgeMs, item.AlreadySurfaced, item.Title, deferred)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "pending=%d oldest_age=%dms open=%d backlog=%d deferred=%d\n", status.Pending, status.OldestAgeMs, status.Open, status.Backlog, status.Deferred)
	} else {
		output.PrintData(status, resolveFormat())
	}
	return nil
}}

func init() {
	feedStatusCmd.Flags().String("cycle", "", "scope status to this cycle; close it with --after-push")
	feedStatusCmd.Flags().Bool("after-push", false, "after delivery, list positive scores from the last 2 hours without a surface report (each listed once)")
	feedCmd.AddCommand(feedStatusCmd)
}
