package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"eigenflux_server/pkg/replaylog"
	"eigenflux_server/rpc/sort/discovery"
)

func TestLegacyPagesRecordOnlyDeliveredRows(t *testing.T) {
	s, e, r := setup(t)
	ctx := context.Background()
	base := e.x.Candidates[0]
	base.FinalScore = .9
	e.x.Candidates = nil
	for i := int64(0); i < 3; i++ {
		c := base
		c.Document.Ref.ID += i
		c.Document.GroupID += i
		e.x.Candidates = append(e.x.Candidates, c)
	}
	var impression string
	for pos, action := range []string{"refresh", "load_more", "load_more"} {
		out, more, err := s.ServePage(ctx, 1, action, nil)
		if err != nil || len(out.Items) != 1 || more != (pos < 2) {
			t.Fatalf("page %d: %+v %v %v", pos, out, more, err)
		}
		if pos == 0 {
			impression = out.ImpressionID
		}
		if out.ImpressionID != impression {
			t.Fatal("impression changed across pages")
		}
		await(t, func() bool { return r.XLen(ctx, replaylog.StreamName).Val() == int64(pos+1) })
		rows, err := r.XRange(ctx, replaylog.StreamName, "-", "+").Result()
		if err != nil || len(rows) != pos+1 {
			t.Fatalf("prefetched sample recorded: %d %v", len(rows), err)
		}
		var delivered []replaylog.ServedItem
		if err = json.Unmarshal([]byte(rows[pos].Values["items"].(string)), &delivered); err != nil {
			t.Fatal(err)
		}
		if len(delivered) != 1 || delivered[0].Position != pos || delivered[0].Score != .9 {
			t.Fatalf("position/score: %+v", delivered)
		}
		if rows[pos].Values["impression_id"] != impression {
			t.Fatal("sample impression mismatch")
		}
	}
	out, more, err := s.ServePage(ctx, 1, "load_more", nil)
	if err != nil || more || len(out.Items) != 0 || e.calls != 1 {
		t.Fatal(out, more, err, e.calls)
	}
}
func TestLegacyPagePreparationFailureDoesNotAdvance(t *testing.T) {
	s, e, r := setup(t)
	ctx := context.Background()
	e.x.Candidates = e.x.Candidates[:1]
	_, _, err := s.ServePage(ctx, 1, "refresh", func(context.Context, *discovery.Execution) error { return fmt.Errorf("hydration failed") })
	if err == nil || r.XLen(ctx, replaylog.StreamName).Val() != 0 || r.Exists(ctx, "discovery:feed:1:page").Val() != 0 {
		t.Fatal("failed page was committed")
	}
}
func TestLegacyPageUsesFrozenContextAndSkipsMissingDetails(t *testing.T) {
	s, e, r := setup(t)
	ctx := context.Background()
	base := e.x.Candidates[0]
	e.x.Candidates = nil
	for i := int64(0); i < 3; i++ {
		c := base
		c.Document.Ref.ID += i
		e.x.Candidates = append(e.x.Candidates, c)
	}
	first, more, err := s.ServePage(ctx, 1, "refresh", nil)
	if err != nil || !more {
		t.Fatal(first, more, err)
	}
	// Changed source state cannot cause a Sort call for the frozen page.
	e.err = discovery.Failure(409, "context_changed")
	prepared := 0
	next, more, err := s.ServePage(ctx, 1, "load_more", func(_ context.Context, x *discovery.Execution) error {
		prepared++
		if x.Candidates[0].Document.Ref.ID == 78 {
			x.Candidates = nil
		}
		return nil
	})
	if err != nil || more || len(next.Items) != 1 || next.Items[0].Ref.ID != 79 || e.calls != 1 || prepared != 2 {
		t.Fatal(next, more, err, e.calls, prepared)
	}
	await(t, func() bool { return r.XLen(ctx, replaylog.StreamName).Val() == 2 })
	entries := r.XRange(ctx, replaylog.StreamName, "-", "+").Val()
	for _, entry := range entries {
		var rows []replaylog.ServedItem
		if err := json.Unmarshal([]byte(entry.Values["items"].(string)), &rows); err != nil {
			t.Fatal(err)
		}
		if rows[0].SourceID == 78 {
			t.Fatal("missing candidate was recorded")
		}
		if rows[0].SourceID == 79 && rows[0].Position != 1 {
			t.Fatal("skipped candidate left a position gap")
		}
	}
}

func TestLegacyPageRecordingFailureDoesNotStopPagination(t *testing.T) {
	s, e, r := setup(t)
	ctx := context.Background()
	r.Set(ctx, replaylog.StreamName, "wrong", 0)
	before := recordingFailures("sample")
	first, more, err := s.ServePage(ctx, 1, "refresh", nil)
	if err != nil || !more {
		t.Fatal(first, more, err)
	}
	next, _, err := s.ServePage(ctx, 1, "load_more", nil)
	if err != nil || next.ImpressionID != first.ImpressionID || next.Items[0].Ref.Type == first.Items[0].Ref.Type || e.calls != 1 {
		t.Fatal(next, err)
	}
	await(t, func() bool {
		return recordingFailures("sample") >= before+2 && r.SIsMember(ctx, "impr:agent:1:items", "77").Val() && r.SIsMember(ctx, "impr:discovery:agent:1:items", "commission:77").Val()
	})
}
