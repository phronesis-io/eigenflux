package feedevent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReportsTransitionsRetentionAndAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	s := NewReportStore(dir, "server/agent")
	now := time.Now().UnixMilli()
	item := Event{"item_id": "1", "impression_id": "imp", "score": int64(2), "client_key": "key"}
	for _, status := range []string{"queued", "accepted", "duplicate", "rejected"} {
		if err := s.Update(now, func(r *Reports) { r.RecordFeedback(item, now, status) }); err != nil {
			t.Fatal(err)
		}
		state, err := s.Read()
		if err != nil {
			t.Fatal(err)
		}
		if state.Entries[reportKey("imp", "1")].Delivery != status {
			t.Fatal(state)
		}
	}
	before, err := os.ReadFile(filepath.Join(dir, "feed-reports.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomicJSON(dir, "feed-reports.json", make(chan int)); err == nil {
		t.Fatal("expected write failure")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "feed-reports.json"))
	if string(before) != string(after) {
		t.Fatal("failed write replaced state")
	}
	if _, err := os.Stat(filepath.Join(dir, ".feed-reports.lock")); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(now+ledgerTTLMs+1, func(*Reports) {}); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Read()
	if len(state.Entries) != 0 {
		t.Fatal(state)
	}
}
func TestPendingRulesAndSurfaceAcrossExposures(t *testing.T) {
	now := time.Now().UnixMilli()
	l := &Ledger{exact: map[string]Entry{}}
	r := Reports{Entries: map[string]Report{}}
	for _, id := range []string{"pending", "baseline", "search", "old", "future", "queued", "accepted", "duplicate", "rejected"} {
		e := Entry{ItemID: id, ImpressionID: "imp", ServedAt: now - 1000, Feed: true, Title: strings.Repeat("界", 80)}
		switch id {
		case "baseline":
			e.Baseline = true
		case "search":
			e.Feed = false
		case "old":
			e.ServedAt = now - PendingWindowMs
		case "future":
			e.ServedAt = now + 1
		case "queued", "accepted", "duplicate", "rejected":
			r.Entries[reportKey("imp", id)] = Report{Delivery: id}
		}
		l.exact[reportKey("imp", id)] = e
	}
	r.Entries[reportKey("older", "pending")] = Report{ItemID: "pending", SurfaceReportedAt: now - 500}
	got := l.Pending(r, now)
	if len(got) != 1 || got[0].ItemID != "pending" || !got[0].AlreadySurfaced {
		t.Fatal(got)
	}
	if got := truncate(strings.Repeat("界", 81), 80); len([]rune(got)) != 80 {
		t.Fatal(got)
	}
}
func TestReportScopeAndHealthWindow(t *testing.T) {
	dir := t.TempDir()
	s := NewReportStore(dir, "a")
	now := time.Now().UnixMilli()
	if err := s.Count(now-HealthWindowMs, map[string]int64{"pulls": 99}); err != nil {
		t.Fatal(err)
	}
	if err := s.Count(now-1, map[string]int64{"pulls": 2}); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Read()
	if got := state.HealthCounts(now)["pulls"]; got != 2 {
		t.Fatal(got)
	}
	other, err := NewReportStore(dir, "b").Read()
	if err != nil || len(other.Health) != 0 {
		t.Fatal(other, err)
	}
}
func TestFeedbackKeyAndLateReceipt(t *testing.T) {
	no, yes := false, true
	a := FeedbackKey("s/a", "imp", "1", 1, nil)
	for _, other := range []string{FeedbackKey("s/b", "imp", "1", 1, nil), FeedbackKey("s/a", "other", "1", 1, nil), FeedbackKey("s/a", "imp", "1", 2, nil), FeedbackKey("s/a", "imp", "1", 1, &no), FeedbackKey("s/a", "imp", "1", 1, &yes)} {
		if other == a {
			t.Fatal("key collision")
		}
	}
	if a != FeedbackKey("s/a", "imp", "1", 1, nil) || len(a) != 32 {
		t.Fatal(a)
	}
	r := Reports{Entries: map[string]Report{}}
	item := Event{"item_id": "1", "impression_id": "imp", "client_key": "new", "score": 1, "ts": int64(100)}
	r.RecordFeedback(item, 100, "queued")
	item["client_key"] = "old"
	item["ts"] = int64(99)
	r.RecordFeedback(item, 101, "accepted")
	if r.Entries[reportKey("imp", "1")].Delivery != "queued" {
		t.Fatal(r)
	}
}
func TestReportConcurrentProcesses(t *testing.T) {
	if dir := os.Getenv("EF_REPORT_CHILD_DIR"); dir != "" {
		s := NewReportStore(dir, "agent")
		for i := 0; i < 30; i++ {
			err := s.Update(time.Now().UnixMilli(), func(r *Reports) {
				id := fmt.Sprintf("%s-%d", os.Getenv("EF_REPORT_CHILD_ID"), i)
				r.Entries[id] = Report{ItemID: id, ServedAt: time.Now().UnixMilli()}
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	dir := t.TempDir()
	children := []*exec.Cmd{}
	for _, id := range []string{"a", "b"} {
		c := exec.Command(os.Args[0], "-test.run=^TestReportConcurrentProcesses$")
		c.Env = append(os.Environ(), "EF_REPORT_CHILD_DIR="+dir, "EF_REPORT_CHILD_ID="+id)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, c)
	}
	for _, c := range children {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	state, err := NewReportStore(dir, "agent").Read()
	if err != nil || len(state.Entries) != 60 {
		t.Fatal(len(state.Entries), err)
	}
}
func TestReportCrashLeavesPreviousFileAndUnlocks(t *testing.T) {
	if dir := os.Getenv("EF_REPORT_CRASH_DIR"); dir != "" {
		l, ok := acquireNamedLock(dir, ".feed-reports.lock")
		if !ok {
			t.Fatal("lock")
		}
		defer l.release()
		if err := os.WriteFile(filepath.Join(dir, ".feed-interrupted.tmp"), []byte(`{"entries":`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	dir := t.TempDir()
	s := NewReportStore(dir, "agent")
	now := time.Now().UnixMilli()
	if err := s.Count(now, map[string]int64{"pulls": 1}); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(os.Args[0], "-test.run=^TestReportCrashLeavesPreviousFileAndUnlocks$")
	c.Env = append(os.Environ(), "EF_REPORT_CRASH_DIR="+dir)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := c.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = c.Wait()
	if err := s.Count(now, map[string]int64{"pulls": 1}); err != nil {
		t.Fatal(err)
	}
	state, err := s.Read()
	if err != nil || state.HealthCounts(now)["pulls"] != 2 {
		t.Fatal(state, err)
	}
}
func TestQueueFeedbackRetryAnd72HourDrop(t *testing.T) {
	q := NewQueue(t.TempDir())
	now := time.Now().UnixMilli()
	if MaxAgeMs != 72*60*60*1000 {
		t.Fatal(MaxAgeMs)
	}
	for _, age := range []int64{MaxAgeMs + 1, MaxAgeMs - 1} {
		if err := q.Enqueue([]Event{{"kind": "feedback", "client_key": fmt.Sprint(age), "ts": now - age}}); err != nil {
			t.Fatal(err)
		}
	}
	drops := 0
	_, remaining, err := q.FlushWithDrops(now, func([]Event) error { return errBoom }, func(n int) error { drops += n; return nil })
	if err == nil || remaining != 1 || drops != 1 {
		t.Fatal(remaining, drops, err)
	}
	q2 := NewQueue(q.dir)
	sent := 0
	_, _, err = q2.Flush(now, func(events []Event) error { sent += len(events); return nil })
	if err != nil || sent != 1 {
		t.Fatal(sent, err)
	}
	raw, _ := os.ReadFile(q.path)
	var state queueFile
	if json.Unmarshal(raw, &state) != nil || len(state.Events) != 0 {
		t.Fatal(string(raw))
	}
}

func TestReportUpgradeDistinguishesBacklogFromOldHandouts(t *testing.T) {
	now := time.Now().UnixMilli()
	var old Report
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"item_id":"1","impression_id":"old","served_at":%d}`, now-100)), &old); err != nil {
		t.Fatal(err)
	}
	if old.HandedTime() != now-100 || old.Hands() != 1 || old.Batched {
		t.Fatal(old)
	}
	old.Hand(now)
	if old.HandedTime() != now || old.Hands() != 2 || !old.Batched {
		t.Fatal(old)
	}
	zero := int64(0)
	backlog := Report{ItemID: "2", ImpressionID: "page", ServedAt: now - 50, HandedAt: &zero}
	encoded, err := json.Marshal(backlog)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded Report
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.HandedTime() != 0 {
		t.Fatal(reloaded)
	}
	l := &Ledger{exact: map[string]Entry{
		"old\x001":  {ItemID: "1", ImpressionID: "old", ServedAt: now - 100, Feed: true},
		"page\x002": {ItemID: "2", ImpressionID: "page", ServedAt: now - 50, Feed: true},
	}}
	if got := l.Pending(Reports{Entries: map[string]Report{"page\x002": reloaded}}, now); len(got) != 1 || got[0].ItemID != "1" {
		t.Fatal(got)
	}
}

func TestLookupHandedEqualTimesPreferNewestCachedResponse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "broadcasts", "20261029")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Truncate(time.Millisecond)
	for _, page := range []struct{ name, impression string }{
		{"feeds-20261029-100000.000000001.json", "older"},
		{"feeds-20261029-100000.000000002.json", "newer"},
	} {
		path := filepath.Join(dir, page.name)
		body := fmt.Sprintf(`{"impression_id":%q,"items":[{"item_id":"7","source_ref":{"type":"broadcast"}}]}`, page.impression)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		// Both responses land in the same millisecond, as rapid discovery calls do.
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	// Map iteration order varies per run; every run must pick the newest response.
	for run := 0; run < 200; run++ {
		entry, status := NewLedger(filepath.Dir(dir), "s").LookupHanded("7", Reports{}, stamp.UnixMilli())
		if status != StatusHit || entry.ImpressionID != "newer" {
			t.Fatalf("run %d: %v %q", run, status, entry.ImpressionID)
		}
	}
}
