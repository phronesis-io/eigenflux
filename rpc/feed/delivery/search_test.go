package delivery

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"eigenflux_server/pkg/replaylog"
	"eigenflux_server/rpc/sort/discovery"
)

func TestSearchPagesFreezeOrderAndRecordOnlyDeliveredItems(t *testing.T) {
	s, executor, redis := setup(t)
	ctx := context.Background()
	r := discovery.Request{Query: "design", Limit: 1, SourceKinds: discovery.AllKinds}
	first, err := s.Serve(ctx, 1, r, discovery.Search, "first-page")
	if err != nil || len(first.Items) != 1 || !first.HasMore || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	await(t, func() bool { return redis.XLen(ctx, replaylog.StreamName).Val() == 1 })
	// Changes after ranking must not cause a new source read on later pages.
	executor.err = discovery.Failure(503, "source_changed")
	again, err := s.Serve(ctx, 1, r, discovery.Search, "first-page")
	if err != nil || !sameResponse(first, again) {
		t.Fatal(again, err)
	}
	page := first
	for pos := 1; pos < 3; pos++ {
		r.Cursor = page.NextCursor
		page, err = s.Serve(ctx, 1, r, discovery.Search, "")
		if err != nil || len(page.Items) != 1 || page.Items[0].Ref.Type != discovery.AllKinds[pos] || page.ImpressionID != first.ImpressionID || page.HasMore != (pos < 2) {
			t.Fatal(page, err)
		}
		if page.HasMore != (page.NextCursor != "") {
			t.Fatal("cursor and has_more disagree")
		}
		again, err = s.Serve(ctx, 1, r, discovery.Search, "")
		if err != nil || !sameResponse(page, again) {
			t.Fatal("cursor retry changed response", again, err)
		}
		await(t, func() bool { return redis.XLen(ctx, replaylog.StreamName).Val() == int64(pos+1) })
	}
	if executor.calls != 1 {
		t.Fatal("pagination re-executed ranking", executor.calls)
	}
	entries := redis.XRange(ctx, replaylog.StreamName, "-", "+").Val()
	for pos, entry := range entries {
		var rows []replaylog.ServedItem
		if err := json.Unmarshal([]byte(entry.Values["items"].(string)), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Position != pos || entry.Values["impression_id"] != first.ImpressionID {
			t.Fatal("prefetched or duplicate exposure", rows)
		}
	}
}

func TestSearchCursorOwnershipBindingAndExpiry(t *testing.T) {
	s, e, redis := setup(t)
	ctx := context.Background()
	r := discovery.Request{Query: "design", Limit: 1}
	first, err := s.Serve(ctx, 1, r, discovery.Search, "")
	if err != nil {
		t.Fatal(err)
	}
	r.Cursor = first.NextCursor
	for _, change := range []func(*discovery.Request){
		func(r *discovery.Request) { r.Query = "other" },
		func(r *discovery.Request) { r.Limit = 2 },
		func(r *discovery.Request) { r.SourceKinds = []discovery.Kind{discovery.Agent} },
		func(r *discovery.Request) { r.Filters.Lang = []string{"en"} },
	} {
		changed := r
		change(&changed)
		_, err := s.Serve(ctx, 1, changed, discovery.Search, "")
		if err == nil || err.Error() != "search_cursor_request_mismatch" {
			t.Fatal("changed request reused cursor", err)
		}
	}
	if _, err := s.Serve(ctx, 2, r, discovery.Search, ""); err == nil || err.Error() != "search_cursor_expired" {
		t.Fatal("cross-owner cursor accepted", err)
	}
	if _, err := s.Serve(ctx, 1, r, discovery.Recommendation, ""); err == nil {
		t.Fatal("recommendation accepted a search cursor")
	}
	for _, cursor := range []string{"broken", strings.Split(r.Cursor, ".")[0] + "." + strings.Repeat("0", 32)} {
		bad := r
		bad.Cursor = cursor
		if _, err := s.Serve(ctx, 1, bad, discovery.Search, ""); err == nil {
			t.Fatal("forged cursor accepted")
		}
	}
	redis.Del(ctx, searchKey(1, strings.Split(r.Cursor, ".")[0]))
	if _, err := s.Serve(ctx, 1, r, discovery.Search, ""); err == nil || err.Error() != "search_cursor_expired" {
		t.Fatal("expired search restarted silently", err)
	}
	if e.calls != 1 {
		t.Fatal("invalid continuation executed another search")
	}
}

func TestConcurrentSearchCursorRetriesRecordOnce(t *testing.T) {
	s, _, redis := setup(t)
	ctx := context.Background()
	r := discovery.Request{Query: "design", Limit: 1}
	first, err := s.Serve(ctx, 1, r, discovery.Search, "")
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return redis.XLen(ctx, replaylog.StreamName).Val() == 1 })
	r.Cursor = first.NextCursor
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := s.Serve(ctx, 1, r, discovery.Search, "")
			if err != nil || len(out.Items) != 1 || out.Items[0].Ref.Type != discovery.Commission {
				t.Error(out, err)
			}
		}()
	}
	wg.Wait()
	await(t, func() bool { return redis.XLen(ctx, replaylog.StreamName).Val() == 2 })
}

func sameResponse(a, b discovery.Response) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}
