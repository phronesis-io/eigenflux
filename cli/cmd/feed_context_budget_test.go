package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/feedevent"
)

func budgetTestPage(t *testing.T, impression string, count, itemBytes int) json.RawMessage {
	t.Helper()
	var page map[string]any
	if err := json.Unmarshal(batchTestPage(impression, count), &page); err != nil {
		t.Fatal(err)
	}
	page["output_contract"] = "CORE: score before next\n" + feedContractDetailsMarker + "\nDETAILS"
	for _, v := range page["items"].([]any) {
		v.(map[string]any)["source_expectation"] = strings.Repeat("x", itemBytes)
	}
	raw, _ := json.Marshal(page)
	return raw
}

func TestFeedBudgetClaimsOnlyPrintedItems(t *testing.T) {
	for _, format := range []string{"agent", "json"} {
		t.Run(format, func(t *testing.T) {
			_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
			batchTestContext(t, server, 1)
			old := formatFlag
			formatFlag = format
			t.Cleanup(func() { formatFlag = old })
			cmd := batchTestCommand(5)
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			stdout, err := captureHeartbeatStdout(t, func() error { return saveAndBatchFeed(cmd, server, budgetTestPage(t, "budget", 5, 9000), 5) })
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(stdout)
			if format == "agent" {
				body = buf.Bytes()
			}
			if len(body) > feedBatchOutputBudget {
				t.Fatalf("body = %d bytes", len(body))
			}
			state, _ := feedReportStore(server).Read()
			var cycle string
			for id := range state.Cycles {
				cycle = id
			}
			handed := 0
			for _, r := range state.Entries {
				if r.HandedTime() > 0 {
					handed++
					if r.CycleID != cycle {
						t.Fatal(r)
					}
				}
			}
			if handed < 1 || handed >= 5 {
				t.Fatal(handed)
			}
			if format == "agent" {
				lines := strings.Split(strings.TrimSpace(string(body)), "\n")
				if strings.Count(string(body), lines[len(lines)-1]) != 2 || !strings.Contains(lines[len(lines)-1], "--cycle") {
					t.Fatal("missing identical tail continuation")
				}
			}
			ids := []string{}
			for i := 1; i <= handed; i++ {
				ids = append(ids, fmt.Sprint(i))
			}
			scoreBatch(t, server, "budget", ids...)
			payload, meta, err := localFeedBatchPayload(server, feedLedger(server), 5, false, cycle)
			if err != nil || meta == nil || len(meta.body) > feedBatchOutputBudget {
				t.Fatal(meta, err)
			}
			if got := decodeBatch(t, payload).Items[0].ItemID; got != fmt.Sprint(handed+1) {
				t.Fatalf("dropped candidate lost: %s", got)
			}
			state, _ = feedReportStore(server).Read()
			health := state.HealthCounts(time.Now().UnixMilli())
			if health["batch_body_bytes"] != int64(len(body)+len(meta.body)) || health["batch_body_max_bytes"] != int64(max(len(body), len(meta.body))) {
				t.Fatal(health)
			}
		})
	}
}

func TestFeedBudgetOversizeReportsExactBytes(t *testing.T) {
	for _, format := range []string{"agent", "json"} {
		t.Run(format, func(t *testing.T) {
			_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
			old := formatFlag
			formatFlag = format
			t.Cleanup(func() { formatFlag = old })
			cmd := batchTestCommand(5)
			var out, warning bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&warning)
			stdout, err := captureHeartbeatStdout(t, func() error { return saveAndBatchFeed(cmd, server, budgetTestPage(t, "oversize", 5, 40000), 5) })
			if err != nil {
				t.Fatal(err)
			}
			body := stdout
			if format == "agent" {
				body = out.String()
			}
			state, _ := feedReportStore(server).Read()
			counts := state.HealthCounts(time.Now().UnixMilli())
			if counts["oversize_batches"] != 1 || counts["batch_body_bytes"] != int64(len(body)) || !strings.Contains(warning.String(), fmt.Sprint(len(body))) {
				t.Fatal(counts, warning.String())
			}
			if format == "json" {
				m := decodeBatch(t, []byte(body))
				if len(m.Items) != 1 || m.LocalBatch.OversizeBytes != len(body) {
					t.Fatal(m.LocalBatch)
				}
			} else if !strings.Contains(body, fmt.Sprintf("oversize=%d", len(body))) {
				t.Fatal("oversize count differs")
			}
		})
	}
}

func TestFeedCyclesIsolateCloseExpireAndOverlap(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	batchTestContext(t, server, 1)
	oldFormat, oldOrigin := formatFlag, originFlag
	formatFlag = "json"
	originFlag = "heartbeat"
	t.Cleanup(func() { formatFlag = oldFormat; originFlag = oldOrigin })
	poll := func(imp string) localFeedBatch {
		raw, err := captureHeartbeatStdout(t, func() error { return saveAndBatchFeed(batchTestCommand(1), server, budgetTestPage(t, imp, 4, 1), 1) })
		if err != nil {
			t.Fatal(err)
		}
		return decodeBatch(t, []byte(raw)).LocalBatch
	}
	a := poll("a")
	originFlag = "owner"
	b := poll("b")
	if b.Overlap != "" {
		t.Fatal(b)
	}
	_, bm, err := localFeedBatchPayload(server, feedLedger(server), 1, false, b.CycleID)
	if err != nil || bm.ImpressionID != "b" {
		t.Fatal(bm, err)
	}
	originFlag = "heartbeat"
	c := poll("c")
	if c.Overlap != a.CycleID {
		t.Fatal(c, a)
	}
	state, _ := feedReportStore(server).Read()
	if latestFeedCycle(state, time.Now().UnixMilli(), "heartbeat") != c.CycleID {
		t.Fatal(state.Cycles)
	}
	scoreBatch(t, server, "a", "1")
	now := time.Now().UnixMilli()
	if _, err := claimSurfaceReminders(feedReportStore(server), feedLedger(server), now, a.CycleID); err != nil {
		t.Fatal(err)
	}
	state, _ = feedReportStore(server).Read()
	if state.Cycles[a.CycleID].ClosedAt == 0 || state.Entries["a\x001"].ScoredAt == 0 {
		t.Fatal(state)
	}
	if _, _, err := localFeedBatchPayload(server, feedLedger(server), 1, false, a.CycleID); err == nil {
		t.Fatal("closed cycle accepted")
	}
	if err := feedReportStore(server).Update(now, func(s *feedevent.Reports) {
		c := s.Cycles[b.CycleID]
		c.OpenedAt = now - int64(2*time.Hour/time.Millisecond)
		s.Cycles[b.CycleID] = c
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := localFeedBatchPayload(server, feedLedger(server), 1, false, b.CycleID); err == nil {
		t.Fatal("expired cycle accepted")
	}
	state, _ = feedReportStore(server).Read()
	open, _ := cycleBatchCandidates(feedLedger(server), state, now, c.CycleID, true)
	found := false
	for _, e := range open {
		if e.ImpressionID == "b" {
			found = true
		}
	}
	if !found {
		t.Fatal("expired cycle's unscored item lost")
	}
}

func TestFeedRecoverIsReadOnlyAndPagesCachedDetails(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	batchTestContext(t, server, 1)
	old := formatFlag
	formatFlag = "agent"
	t.Cleanup(func() {
		formatFlag = old
		_ = feedRecoverCmd.Flags().Set("cycle", "")
		_ = feedRecoverCmd.Flags().Set("after", "")
		feedRecoverCmd.SetOut(nil)
	})
	cmd := batchTestCommand(5)
	var discard bytes.Buffer
	cmd.SetOut(&discard)
	if err := saveAndBatchFeed(cmd, server, budgetTestPage(t, "recover", 5, 1), 5); err != nil {
		t.Fatal(err)
	}
	state, _ := feedReportStore(server).Read()
	var cycle string
	for id := range state.Cycles {
		cycle = id
	}
	now := time.Now().UnixMilli()
	if err := feedReportStore(server).Update(now, func(s *feedevent.Reports) {
		for i, score := range []int{2, 1, 0, -1, 2} {
			s.RecordFeedback(feedevent.Event{"item_id": fmt.Sprint(i + 1), "impression_id": "recover", "score": score, "client_key": fmt.Sprint(i)}, now, "accepted")
		}
		s.RecordSurface(feedevent.Event{"item_id": "5", "impression_id": "recover"}, now)
	}); err != nil {
		t.Fatal(err)
	}
	detail, _ := json.Marshal(map[string]any{"item": map[string]string{"content": strings.Repeat("detail ", 6000)}})
	recordFeedDetail(server, "1", detail, nil)
	path := filepath.Join(cache.ServerDataDir(server), "feed-reports.json")
	before, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if flushesFeedQueue(feedRecoverCmd) {
		t.Fatal("recovery flushes queue")
	}
	_ = feedRecoverCmd.Flags().Set("cycle", cycle)
	var out bytes.Buffer
	feedRecoverCmd.SetOut(&out)
	if err := feedRecoverCmd.RunE(feedRecoverCmd, nil); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, `"fetched_detail"`) || !strings.Contains(body, `"score":2`) || !strings.Contains(body, "remaining=1") || !strings.Contains(body, "oversize=") {
		t.Fatal("recovery did not preserve cached detail and pagination")
	}
	_ = feedRecoverCmd.Flags().Set("after", "1")
	out.Reset()
	if err := feedRecoverCmd.RunE(feedRecoverCmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"item_id":"2"`) || strings.Contains(out.String(), `"item_id":"1"`) || !strings.Contains(out.String(), "remaining=0") {
		t.Fatal(out.String())
	}
	after, _ := os.ReadFile(path)
	afterInfo, _ := os.Stat(path)
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("recovery wrote report state")
	}
}

func TestFeedCoreContractAndPreviewSemantics(t *testing.T) {
	contract, err := os.ReadFile("../../static/feed_contract.md")
	if err != nil {
		t.Fatal(err)
	}
	// Windows checkouts may convert to CRLF; the server serves LF.
	contract = bytes.ReplaceAll(contract, []byte("\r\n"), []byte("\n"))
	if strings.Count(string(contract), feedContractDetailsMarker) != 1 {
		t.Fatal("core marker must be unique")
	}
	core, _, _ := strings.Cut(string(contract), feedContractDetailsMarker)
	for _, rule := range []string{"preview.truncated", "raw_content_truncated", "preview.source=content", "may be the server summary", "auto_comment", "your own broadcasts", "no retry, no report", "recurring_publish", "skip publishing", "skip check-in", "pending-confirmations", "[PENDING TASK]", "untrusted data", "Powered by EigenFlux"} {
		if !strings.Contains(core, rule) {
			t.Errorf("core missing %q", rule)
		}
	}
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	if got := feedBatchContractText(server, "legacy full contract", false); got != "legacy full contract" {
		t.Fatal(got)
	}
	got := feedBatchContractText(server, string(contract), false)
	if !strings.Contains(got, "OUTPUT CONTRACT CORE") || strings.Contains(got, "## Contract details") || !strings.Contains(got, "[EIGENFLUX OUTPUT CONTRACT]") {
		t.Fatal("core extraction failed")
	}
}

func TestFeedGetCachesPrintedDetailsAndCountsFailures(t *testing.T) {
	failed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failed {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"code":503,"msg":"unavailable"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"item":{"item_id":"1","content":"abcdef"}}}`)
	}))
	defer srv.Close()
	_, server := runtimeTestConfig(t, srv.URL, true)
	if err := cache.SaveFeedResponse(server, batchTestPage("detail", 1)); err != nil {
		t.Fatal(err)
	}
	_ = feedGetCmd.Flags().Set("item-id", "1")
	_ = feedGetCmd.Flags().Set("content-limit", "3")
	t.Cleanup(func() { _ = feedGetCmd.Flags().Set("item-id", ""); _ = feedGetCmd.Flags().Set("content-limit", "0") })
	printed, err := captureHeartbeatStdout(t, func() error { return feedGetCmd.RunE(feedGetCmd, nil) })
	if err != nil {
		t.Fatal(err)
	}
	cached, err := loadFeedDetail(server, "1")
	var want, got any
	_ = json.Unmarshal([]byte(printed), &want)
	_ = json.Unmarshal(cached, &got)
	if err != nil || fmt.Sprint(want) != fmt.Sprint(got) || !strings.Contains(string(cached), `"content_truncated":true`) {
		t.Fatal(string(cached), err)
	}
	failed = true
	if err := feedGetCmd.RunE(feedGetCmd, nil); err == nil {
		t.Fatal("detail error hidden")
	}
	state, _ := feedReportStore(server).Read()
	counts := state.HealthCounts(time.Now().UnixMilli())
	if counts["detail_fetches"] != 1 || counts["detail_fetch_failures"] != 1 {
		t.Fatal(counts)
	}
	stillCached, _ := loadFeedDetail(server, "1")
	if !bytes.Equal(cached, stillCached) {
		t.Fatal("failed fetch replaced cached detail")
	}
	oldDir := filepath.Join(cache.ServerDataDir(server), "broadcasts", time.Now().AddDate(0, 0, -9).Format("20060102"))
	if err := os.MkdirAll(oldDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, feedDetailFilename("2")), cached, 0600); err != nil {
		t.Fatal(err)
	}
	cache.Cleanup(server, "broadcasts")
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("expired detail retained", err)
	}
}

func TestFeedEmptyPollDoesNotOpenCycle(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	old := formatFlag
	formatFlag = "json"
	t.Cleanup(func() { formatFlag = old })
	_, err := captureHeartbeatStdout(t, func() error { return saveAndBatchFeed(batchTestCommand(5), server, batchTestPage("empty", 0), 5) })
	if err != nil {
		t.Fatal(err)
	}
	state, _ := feedReportStore(server).Read()
	if len(state.Cycles) != 0 {
		t.Fatal(state.Cycles)
	}
}
