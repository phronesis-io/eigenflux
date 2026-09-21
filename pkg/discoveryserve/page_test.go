package discoveryserve

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"eigenflux_server/pkg/discovery"
	"eigenflux_server/pkg/replaylog"
)

func (e *executor) RefreshPage(ctx context.Context, owner int64, x discovery.Execution, now int64) (discovery.Execution, error) {
	x.Mode = discovery.Recommendation
	return x, e.Revalidate(ctx, owner, x, now)
}

func TestLegacyPagesCommitOnlyDeliveredRows(t *testing.T) {
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
func TestLegacyPageInvalidatesChangedContext(t *testing.T) {
	s, e, r := setup(t)
	ctx := context.Background()
	if _, _, err := s.ServePage(ctx, 1, "refresh", nil); err != nil {
		t.Fatal(err)
	}
	e.stale = true
	if _, _, err := s.ServePage(ctx, 1, "load_more", nil); err == nil {
		t.Fatal("changed context accepted")
	}
	if r.Exists(ctx, "discovery:feed:1:page").Val() != 0 || r.XLen(ctx, replaylog.StreamName).Val() != 1 {
		t.Fatal("stale page advanced")
	}
}
