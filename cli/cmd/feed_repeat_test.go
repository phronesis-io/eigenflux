package cmd

import (
	"cli.eigenflux.ai/internal/feedevent"
	"fmt"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReusableFeedPage(t *testing.T) {
	now := time.Now().UnixMilli()
	for _, tc := range []struct {
		name, action, origin    string
		age                     int64
		baseline, scored, empty bool
		want                    bool
	}{
		{name: "unprocessed", age: 1, want: true}, {name: "boundary", age: 60000},
		{name: "future", age: -1}, {name: "more", action: "more"}, {name: "owner", origin: "owner"},
		{name: "baseline", baseline: true}, {name: "scored", scored: true}, {name: "empty", empty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := feedevent.Reports{LastFetchAt: now - tc.age, LastImpressionID: "p", Entries: map[string]feedevent.Report{}}
			if !tc.empty {
				r := feedevent.Report{ImpressionID: "p", Baseline: tc.baseline}
				if tc.scored {
					r.ScoredAt = now
				}
				state.Entries["p\x001"] = r
			}
			if _, got := reusableFeedPage(tc.action, tc.origin, state, now); got != tc.want {
				t.Fatalf("reuse=%v", got)
			}
		})
	}
}

func TestFeedRepeatKeepsPageAndFetchTime(t *testing.T) {
	pulls, heartbeats, revision := 0, 0, 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/agent-context":
			fmt.Fprintf(w, `{"data":{"context_revision":%d,"control_context":{"context_revision":%d}}}`, revision, revision)
		case "/api/v2/runtime/heartbeat":
			heartbeats++
			fmt.Fprint(w, `{"data":{}}`)
		case "/api/v2/feed":
			pulls++
			fmt.Fprintf(w, `{"data":{"schema_version":"feed.v2","impression_id":"p%d","personalization":{"mode":"intent_aligned","context_revision":%d},"items":[{"item_id":"1"}],"notifications":[],"output_contract":"test"}}`, pulls, revision)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	_, server := runtimeTestConfig(t, srv.URL, true)
	oldFormat, oldOrigin := formatFlag, originFlag
	formatFlag, originFlag = "json", "heartbeat"
	t.Cleanup(func() { formatFlag, originFlag = oldFormat, oldOrigin })
	cmd := &cobra.Command{}
	cmd.Flags().Int("batch", 0, "")
	cmd.Flags().String("action", "refresh", "")
	poll := func() string {
		t.Helper()
		s, err := captureHeartbeatStdout(t, func() error { return pollFeedV2(cmd, server, "20") })
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	first := poll()
	// A preceding batch can leave part of this page unhanded.
	if err := feedReportStore(server).Update(time.Now().UnixMilli(), func(state *feedevent.Reports) {
		for key, entry := range state.Entries {
			zero := int64(0)
			entry.HandedAt = &zero
			entry.Batched = true
			state.Entries[key] = entry
		}
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := feedReportStore(server).Read()
	for i := 0; i < 3; i++ {
		if got := poll(); got != first {
			t.Fatalf("changed output\n%s\n%s", first, got)
		}
	}
	after, _ := feedReportStore(server).Read()
	for _, entry := range after.Entries {
		if entry.HandedTime() == 0 || entry.Batched {
			t.Fatal("whole-page reuse left items unhanded", entry)
		}
	}
	if pulls != 1 || heartbeats != 4 || after.LastFetchAt != before.LastFetchAt || after.HealthCounts(time.Now().UnixMilli())["repeat_pulls_suppressed"] != 3 {
		t.Fatal(pulls, heartbeats, after)
	}
	scoreBatch(t, server, "p1", "1")
	poll()
	if pulls != 2 {
		t.Fatal("scoring must permit next page")
	}
	revision = 2
	poll()
	if pulls != 3 {
		t.Fatal("context change must fetch")
	}
	now := time.Now().UnixMilli()
	if err := feedReportStore(server).Update(now, func(r *feedevent.Reports) { r.LastFetchAt = now - 60000 }); err != nil {
		t.Fatal(err)
	}
	poll()
	if pulls != 4 {
		t.Fatal("reuse extended fetch window")
	}
	originFlag = "owner"
	poll()
	if pulls != 5 {
		t.Fatal("owner did not fetch")
	}
	originFlag = "heartbeat"
	_ = cmd.Flags().Set("action", "more")
	poll()
	if pulls != 6 {
		t.Fatal("more did not fetch")
	}
}
