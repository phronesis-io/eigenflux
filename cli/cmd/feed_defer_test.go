package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/feedevent"
)

func TestFeedDeferredItemSurvivesCycleCloseAndEmptyRefresh(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	batchTestContext(t, server, 1)
	oldFormat := formatFlag
	formatFlag = "json"
	t.Cleanup(func() { formatFlag = oldFormat })
	page := batchTestPage("original", 3)
	raw, err := captureHeartbeatStdout(t, func() error { return saveAndBatchFeed(batchTestCommand(1), server, page, 1) })
	if err != nil {
		t.Fatal(err)
	}
	first := decodeBatch(t, []byte(raw)).LocalBatch
	if first.CycleID == "" || !strings.Contains(first.line(), "defer_supported=true") {
		t.Fatal(first.line())
	}
	for i := 1; i < feedRehandLimit; i++ {
		body, _, err := localFeedBatchPayload(server, feedLedger(server), 1, false, first.CycleID)
		if err != nil || decodeBatch(t, body).Items[0].ItemID != "1" {
			t.Fatal(string(body), err)
		}
	}
	// Explicit deferral remains possible after the last transport retry.
	items := []feedDeferItem{{ItemID: "1", ImpressionID: "original"}}
	deferItems := func() {
		t.Helper()
		n, err := deferFeedItems(feedReportStore(server), feedLedger(server), first.CycleID, items, time.Now().UnixMilli())
		if err != nil || n != 1 {
			t.Fatal(n, err)
		}
	}
	deferItems()
	deferItems()
	store := feedReportStore(server)
	state, err := store.Read()
	if err != nil || state.HealthCounts(time.Now().UnixMilli())["deferred_items"] != 1 {
		t.Fatal(state.Health, err)
	}
	if err := feedStatusCmd.Flags().Set("cycle", first.CycleID); err != nil {
		t.Fatal(err)
	}
	formatFlag = "agent"
	raw, err = captureHeartbeatStdout(t, func() error { return feedStatusCmd.RunE(feedStatusCmd, nil) })
	formatFlag = "json"
	_ = feedStatusCmd.Flags().Set("cycle", "")
	if err != nil || !strings.Contains(raw, "item_id=1 impression_id=original") ||
		!strings.Contains(raw, "deferred=true") || !strings.Contains(raw, "open=0 backlog=2 deferred=1") {
		t.Fatal(raw, err)
	}
	body, other, err := localFeedBatchPayload(server, feedLedger(server), 1, false, first.CycleID)
	if err != nil || other == nil || other.Batch != 4 || decodeBatch(t, body).Items[0].ItemID != "2" {
		t.Fatal(string(body), other, err)
	}
	scoreBatch(t, server, "original", "2")
	if _, err := claimSurfaceReminders(store, feedLedger(server), time.Now().UnixMilli(), first.CycleID); err != nil {
		t.Fatal(err)
	}
	deferItems()

	// Reopen the ledger after closing the four-batch cycle. A new empty
	// server response must leave a usable next command and its notification.
	raw, err = captureHeartbeatStdout(t, func() error {
		return saveAndBatchFeed(batchTestCommand(1), server, batchTestPage("empty-refresh", 0), 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh := decodeBatch(t, []byte(raw))
	if len(fresh.Items) != 0 || len(fresh.Notifications) != 1 || fresh.LocalBatch.CycleID == "" ||
		fresh.LocalBatch.CycleID == first.CycleID || fresh.LocalBatch.Open != 1 || fresh.LocalBatch.Backlog != 1 {
		t.Fatal(raw)
	}
	body, resumed, err := localFeedBatchPayload(server, feedLedger(server), 1, false, fresh.LocalBatch.CycleID)
	if err != nil || resumed == nil || resumed.ImpressionID != "original" || decodeBatch(t, body).Items[0].ItemID != "1" {
		t.Fatal(string(body), resumed, err)
	}
	state, err = feedReportStore(server).Read()
	report := state.Entries["original\x001"]
	if err != nil || report.Hands() != 1 || report.DeferredAt != 0 || report.ScoredAt != 0 {
		t.Fatal(report, err)
	}
	scoreBatch(t, server, "original", "1")
	state, err = feedReportStore(server).Read()
	report = state.Entries["original\x001"]
	if err != nil || report.ScoredAt == 0 || report.Score != 0 || len(feedLedger(server).Pending(state, time.Now().UnixMilli())) != 0 {
		t.Fatal(report, err)
	}
	body, next, err := localFeedBatchPayload(server, feedLedger(server), 1, false, fresh.LocalBatch.CycleID)
	if err != nil || next == nil || decodeBatch(t, body).Items[0].ItemID != "3" {
		t.Fatal(string(body), next, err)
	}
}

func TestFeedDeferRejectsInvalidExposureBatchAtomically(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	batchTestContext(t, server, 1)
	oldFormat := formatFlag
	formatFlag = "json"
	t.Cleanup(func() { formatFlag = oldFormat })
	raw, err := captureHeartbeatStdout(t, func() error {
		return saveAndBatchFeed(batchTestCommand(2), server, batchTestPage("scoped", 3), 2)
	})
	if err != nil {
		t.Fatal(err)
	}
	cycle := decodeBatch(t, []byte(raw)).LocalBatch.CycleID
	scoreBatch(t, server, "scoped", "2")
	now := time.Now().UnixMilli()
	store := feedReportStore(server)
	ledger := feedLedger(server)
	for _, invalid := range []feedDeferItem{
		{ItemID: "1", ImpressionID: "different"},
		{ItemID: "2", ImpressionID: "scoped"},
		{ItemID: "3", ImpressionID: "scoped"},
	} {
		n, err := deferFeedItems(store, ledger, cycle, []feedDeferItem{{ItemID: "1", ImpressionID: "scoped"}, invalid}, now)
		if err == nil || n != 0 {
			t.Fatalf("accepted invalid exposure %+v: %d, %v", invalid, n, err)
		}
	}
	state, err := store.Read()
	if err != nil || state.Entries["scoped\x001"].DeferredAt != 0 || state.HealthCounts(now)["deferred_items"] != 0 {
		t.Fatal(state, err)
	}
	if n, err := deferFeedItems(store, ledger, "another-cycle", []feedDeferItem{{ItemID: "1", ImpressionID: "scoped"}}, now); err == nil || n != 0 {
		t.Fatal(n, err)
	}
	if flushesFeedQueue(feedDeferCmd) {
		t.Fatal("feed defer must not flush network feedback")
	}
}

func TestFeedDeferredExpiryIsReportedOnce(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	batchTestContext(t, server, 1)
	oldFormat := formatFlag
	formatFlag = "json"
	t.Cleanup(func() { formatFlag = oldFormat })
	raw, err := captureHeartbeatStdout(t, func() error {
		return saveAndBatchFeed(batchTestCommand(1), server, batchTestPage("expiring", 1), 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	cycle := decodeBatch(t, []byte(raw)).LocalBatch.CycleID
	store := feedReportStore(server)
	ledger := feedLedger(server)
	now := time.Now().UnixMilli()
	if n, err := deferFeedItems(store, ledger, cycle, []feedDeferItem{{ItemID: "1", ImpressionID: "expiring"}}, now); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := store.Update(now, func(r *feedevent.Reports) {
		entry := r.Entries["expiring\x001"]
		entry.ServedAt = now - feedBacklogAge.Milliseconds()
		r.Entries["expiring\x001"] = entry
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		raw, err = captureHeartbeatStdout(t, func() error { return feedStatusCmd.RunE(feedStatusCmd, nil) })
		if err != nil {
			t.Fatal(err)
		}
		var status feedStatus
		if err := json.Unmarshal([]byte(raw), &status); err != nil || status.Pending != 0 || status.Deferred != 0 || status.Open != 0 ||
			status.Health["deferred_expired_items"] != 1 {
			t.Fatal(raw, err)
		}
	}
}

func TestFeedEmptyRefreshWithoutSavedWorkKeepsWholePage(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	oldFormat := formatFlag
	formatFlag = "json"
	t.Cleanup(func() { formatFlag = oldFormat })
	raw, err := captureHeartbeatStdout(t, func() error {
		return saveAndBatchFeed(batchTestCommand(1), server, batchTestPage("empty", 0), 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	page := decodeBatch(t, []byte(raw))
	if len(page.Notifications) != 1 || strings.Contains(raw, "local_batch") {
		t.Fatal(raw)
	}
	state, err := feedReportStore(server).Read()
	if err != nil || len(state.Cycles) != 0 || state.HealthCounts(time.Now().UnixMilli())["pulls"] != 1 {
		t.Fatal(state, err)
	}
}
