package discoveryserve

import (
	"context"
	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/discovery"
	"eigenflux_server/pkg/replaylog"
	"encoding/json"
	"fmt"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"sync/atomic"
	"testing"
	"time"
)

type ids struct{ n atomic.Int64 }

func (i *ids) NextID() (int64, error) { return i.n.Add(1), nil }

type executor struct {
	x     discovery.Execution
	calls int
	stale bool
}

func (e *executor) Execute(_ context.Context, _ int64, _ discovery.Request, m discovery.Mode, _ int64) (discovery.Execution, error) {
	e.calls++
	e.x.Mode = m
	return e.x, nil
}
func (e *executor) Revalidate(context.Context, int64, discovery.Execution, int64) error {
	if e.stale {
		return discovery.Failure(409, "stale_result")
	}
	return nil
}
func setup(t *testing.T) (Service, *executor, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { r.Close() })
	now := time.Now().UnixMilli()
	c := discovery.Context{ID: 44, OwnerID: 1, Revision: 1, State: "active", Origin: "query", Kinds: discovery.AllKinds, CreatedAt: now}
	x := discovery.Execution{Contexts: []discovery.Context{c}, Status: "ok"}
	for _, kind := range discovery.AllKinds {
		x.Candidates = append(x.Candidates, discovery.Candidate{Context: c, Document: discovery.Document{Ref: discovery.SourceRef{Type: kind, ID: 77}, GroupID: 88, AuthorID: 2, Preview: "result"}, Score: discovery.Score{Value: .9, Eligible: true, Version: "test", ScorerType: "rules"}})
	}
	e := &executor{x: x}
	return Service{Redis: r, IDs: &ids{}, Executor: e}, e, r
}
func TestSearchCommitAndIdempotency(t *testing.T) {
	s, e, r := setup(t)
	ctx := context.Background()
	request := discovery.Request{Query: "test"}
	a, err := s.Serve(ctx, 1, request, discovery.Search, "key")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Serve(ctx, 1, request, discovery.Search, "key")
	if err != nil || a.ImpressionID != b.ImpressionID || e.calls != 1 {
		t.Fatalf("retry: %#v %v calls=%d", b, err, e.calls)
	}
	if r.Exists(ctx, "impr:agent:1:items", bloomfilter.GetKeyForDate(time.Now()), "impr:discovery:agent:1:items").Val() != 0 {
		t.Fatal("search polluted automatic history")
	}
	if !r.SIsMember(ctx, "impr:search:agent:1:items", "77").Val() {
		t.Fatal("missing feedback validation")
	}
	entries, err := r.XRange(ctx, replaylog.StreamName, "-", "+").Result()
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	var rows []replaylog.ServedItem
	if err = json.Unmarshal([]byte(entries[0].Values["items"].(string)), &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if err = row.Validate(); err != nil {
			t.Fatal(err)
		}
		if row.SourceKind != "broadcast" && row.ItemID != 0 {
			t.Fatal("typed ID collision")
		}
	}
	_, err = s.Serve(ctx, 1, discovery.Request{Query: "different"}, discovery.Search, "key")
	if err == nil {
		t.Fatal("missing conflict")
	}
	e.stale = true
	if _, err = s.Serve(ctx, 1, request, discovery.Search, "key"); err == nil {
		t.Fatal("stale cached result replayed")
	}
}
func TestAutomaticWritesExistingHistory(t *testing.T) {
	s, _, r := setup(t)
	ctx := context.Background()
	_, err := s.Serve(ctx, 1, discovery.Request{}, discovery.Recommendation, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"impr:agent:1:items", "77"}, {"impr:agent:1:groups", "88"}, {bloomfilter.GetKeyForDate(time.Now()), "1:88"}, {"impr:discovery:agent:1:items", "commission:77"}, {"impr:discovery:agent:1:items", "agent:77"}} {
		if !r.SIsMember(ctx, pair[0], pair[1]).Val() {
			t.Fatal(pair)
		}
	}
	if r.Exists(ctx, "impr:search:agent:1:items").Val() != 0 {
		t.Fatal("automatic in search history")
	}
}
func TestCommitFailsBeforeHistoryOnWrongStreamType(t *testing.T) {
	s, _, r := setup(t)
	ctx := context.Background()
	r.Set(ctx, replaylog.StreamName, "wrong", 0)
	_, err := s.Serve(ctx, 1, discovery.Request{Query: "x"}, discovery.Search, "key")
	if err == nil {
		t.Fatal("expected error")
	}
	keys, _ := r.Keys(ctx, "impr:*").Result()
	if len(keys) > 0 {
		t.Fatal(fmt.Sprint(keys))
	}
}
