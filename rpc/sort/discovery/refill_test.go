package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/impr"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type refillFake struct {
	*sourceFake
	muRefill                  sync.Mutex
	pool                      []Document
	groups                    []int64
	historyCalls, refillCalls int
	incomplete                bool
	historyErr, recallErr     error
	excludedIDs               []int64
}

func (s *refillFake) ConsumedGroups(context.Context, int64) ([]int64, bool, error) {
	s.historyCalls++
	return s.groups, !s.incomplete, s.historyErr
}
func (s *refillFake) RecallUnseen(_ context.Context, _ Context, groups, ids []int64, limit int) ([]Document, error) {
	s.muRefill.Lock()
	defer s.muRefill.Unlock()
	s.refillCalls++
	s.excludedIDs = append([]int64(nil), ids...)
	seen := map[int64]bool{}
	recalled := map[int64]bool{}
	for _, id := range groups {
		seen[id] = true
	}
	for _, id := range ids {
		recalled[id] = true
	}
	out := []Document{}
	for _, d := range s.pool {
		if !seen[d.GroupID] && !recalled[d.Ref.ID] && len(out) < limit {
			out = append(out, d)
		}
	}
	return out, s.recallErr
}
func refillFixture() (*Engine, *refillFake) {
	e, s, _ := engineFixture()
	s.owner = OwnerContext{Clauses: []string{"software"}}
	f := &refillFake{sourceFake: s}
	e.Sources = f
	e.ConsumedRefillBPS = 10000
	return e, f
}
func refillDoc(id int64) Document {
	return Document{Ref: SourceRef{Type: Broadcast, ID: id}, GroupID: id, Active: true, Visible: true, AuthorID: 99, Version: "v1", Lexical: 5, FreshAt: 1000}
}
func runRefill(t *testing.T, e *Engine, limit int) Execution {
	t.Helper()
	x, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}, Limit: limit}, Recommendation, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return x
}
func TestConsumedRefillSequentialAndSwitch(t *testing.T) {
	e, s := refillFixture()
	for i := int64(1); i <= 80; i++ {
		s.docs = append(s.docs, refillDoc(i))
		s.seen[fmt.Sprintf("broadcast:%d", i)] = true
		s.groups = append(s.groups, i)
	}
	for i := int64(1); i <= 115; i++ {
		s.pool = append(s.pool, refillDoc(i))
	}
	e.ConsumedRefillBPS = 0
	if x := runRefill(t, e, 20); len(x.Candidates) != 0 || x.Refill != nil || s.historyCalls != 0 {
		t.Fatalf("off path changed: %+v", x)
	}
	e.ConsumedRefillBPS = 10000
	delivered := map[int64]bool{}
	for _, want := range []int{20, 15, 0} {
		x := runRefill(t, e, 20)
		if len(x.Candidates) != want {
			t.Fatalf("got %d want %d: %+v", len(x.Candidates), want, x)
		}
		for _, c := range x.Candidates {
			id := c.Document.Ref.ID
			if delivered[id] {
				t.Fatalf("duplicate group %d", id)
			}
			delivered[id] = true
			s.groups = append(s.groups, id)
			s.seen[c.Document.Ref.Key()] = true
		}
	}
	if s.refillCalls != 3 || len(s.excludedIDs) != 80 {
		t.Fatalf("unbounded or missing wave: calls=%d exclusions=%d", s.refillCalls, len(s.excludedIDs))
	}
}

func TestConsumedRefillPreservesCappedPageAndRecoversEmptyWithNeedPriority(t *testing.T) {
	e, s := refillFixture()
	e.SourceLimits = []SourceLimit{{Source: "friend", Numerator: 1, Denominator: 2}}
	e.frozenContexts = []Context{
		{ID: 1, OwnerID: 1, State: "active", Kinds: []Kind{Broadcast}, Origin: "friend"},
		{ID: 2, OwnerID: 1, State: "active", Kinds: []Kind{Broadcast}, Origin: "agent_context", Query: "software"},
		{ID: 3, OwnerID: 1, State: "active", Kinds: []Kind{Broadcast}, Origin: "need_input", Priority: 1, Query: "software"},
	}
	s.channels = map[string][]Document{}
	for id := int64(1); id <= 200; id++ {
		d := refillDoc(id)
		d.Lexical = 0
		s.channels["friend"] = append(s.channels["friend"], d)
	}
	for id := int64(201); id <= 280; id++ {
		s.pool = append(s.pool, refillDoc(id))
	}
	run := func() (Execution, []Candidate) {
		x, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}, Limit: 20, Prefetch: true}, Recommendation, 1000)
		if err != nil {
			t.Fatal(err)
		}
		page, _ := SelectRecommendationPage(x.Candidates, 20, x.SourceLimits)
		return x, page
	}
	e.ConsumedRefillBPS = 0
	before, page := run()
	if len(before.Candidates) != 200 || len(page) != 10 {
		t.Fatalf("fixture must fill snapshot but underfill capped page: %d/%d", len(before.Candidates), len(page))
	}
	e.ConsumedRefillBPS = 10000
	after, page := run()
	if !reflect.DeepEqual(before, after) || len(page) != 10 || s.historyCalls != 0 || s.refillCalls != 0 {
		t.Fatal("refill changed a nonempty source-capped page")
	}
	for _, d := range s.channels["friend"] {
		s.seen[d.Ref.Key()] = true
		s.groups = append(s.groups, d.GroupID)
	}
	after, page = run()
	if len(page) != 20 || after.Refill == nil || after.Refill.Contexts != 2 || s.refillCalls != 2 {
		t.Fatalf("capped first page not refilled once per eligible context: %+v", after.Refill)
	}
	friends := 0
	for _, c := range page {
		if c.Document.Ref.ID <= 200 {
			friends++
		} else if c.Context.ID != 3 {
			t.Fatal("refill displaced the winning Need with Profile attribution")
		}
	}
	if friends > 10 {
		t.Fatal("refill bypassed the per-Agent friend source cap")
	}
}
func TestConsumedRefillPreservesEveryNonemptyPage(t *testing.T) {
	for _, prefetch := range []bool{false, true} {
		for _, count := range []int{1, 19, 20, 21} {
			t.Run(fmt.Sprintf("prefetch=%t/count=%d", prefetch, count), func(t *testing.T) {
				e, s := refillFixture()
				for id := int64(1); id <= int64(count); id++ {
					s.docs = append(s.docs, refillDoc(id))
				}
				s.pool = []Document{refillDoc(100)}
				run := func() Execution {
					*e.IDs.(*countIDs) = 100
					x, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}, Limit: 20, Prefetch: prefetch}, Recommendation, 1000)
					if err != nil {
						t.Fatal(err)
					}
					return x
				}
				e.ConsumedRefillBPS = 0
				before := run()
				e.ConsumedRefillBPS = 10000
				after := run()
				if !reflect.DeepEqual(before, after) || s.historyCalls != 0 || s.refillCalls != 0 {
					t.Fatal("nonempty page changed or read optional dependencies")
				}
			})
		}
	}
}

func TestConsumedRefillKeepsGatesAndReportsFailure(t *testing.T) {
	for _, mode := range []string{"history", "recall", "gates"} {
		t.Run(mode, func(t *testing.T) {
			e, s := refillFixture()
			s.docs = []Document{refillDoc(1)}
			s.seen["broadcast:1"] = true
			s.pool = []Document{refillDoc(2), refillDoc(3), refillDoc(4), refillDoc(5)}
			s.pool[0].Visible = false
			s.pool[1].Lexical = 0
			s.pool[2].ExpiresAt = 999
			s.seen[s.pool[3].Ref.Key()] = true
			switch mode {
			case "history":
				s.historyErr = errors.New("offline")
			case "recall":
				s.recallErr = errors.New("offline")
			case "gates":
				s.incomplete = true
			}
			x := runRefill(t, e, 20)
			if len(x.Candidates) != 0 {
				t.Fatalf("refill bypassed gates: %+v", x)
			}
			if len(x.PartialReasons) == 0 {
				t.Fatal("missing degraded outcome")
			}
		})
	}
}
func TestConsumedRefillBucketStableAndNested(t *testing.T) {
	n := 0
	for id := int64(1); id <= 10000; id++ {
		if consumedRefillEnabled(id, 0) || consumedRefillEnabled(id, 10001) || !consumedRefillEnabled(id, 10000) {
			t.Fatal("invalid boundary")
		}
		if consumedRefillEnabled(id, 1000) {
			n++
			if !consumedRefillEnabled(id, 2000) {
				t.Fatal("bucket not nested")
			}
		}
	}
	if n < 800 || n > 1200 {
		t.Fatalf("unexpected allocation %d", n)
	}
}

func TestConsumedRefillDoesNotClaimExhaustiveSupplyAtScanBound(t *testing.T) {
	e, s := refillFixture()
	for id := int64(1); id <= 80; id++ {
		d := refillDoc(id)
		d.Visible = false
		s.pool = append(s.pool, d)
	}
	x := runRefill(t, e, 20)
	if len(x.Candidates) != 0 || x.Refill == nil || !x.Refill.LimitReached {
		t.Fatalf("invalid bounded result: %+v", x)
	}
	found := false
	for _, reason := range x.PartialReasons {
		found = found || reason == "consumed_refill:scan_budget"
	}
	if !found {
		t.Fatal("a full rejected refill page must not imply proven empty supply")
	}
}
func TestConsumedGroupsUsesRollingAuthorityAndBound(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer r.Close()
	s := &Source{Redis: r}
	ctx := context.Background()
	key := fmt.Sprintf(impr.KeyGroupIDs, 1)
	if err := r.SAdd(ctx, key, 11, 12).Err(); err != nil {
		t.Fatal(err)
	}
	if err := bloomfilter.NewBloomFilter(r).Add(ctx, 1, []int64{11}); err != nil {
		t.Fatal(err)
	}
	groups, complete, err := s.ConsumedGroups(ctx, 1)
	if err != nil || !complete || !reflect.DeepEqual(groups, []int64{11}) {
		t.Fatalf("unverified history excluded: %v %v %v", groups, complete, err)
	}
	values := make([]any, maxRefillHistory+1)
	for i := range values {
		values[i] = i + 1
	}
	if err := r.SAdd(ctx, key, values...).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ConsumedGroups(ctx, 1); err == nil {
		t.Fatal("oversized history not bounded")
	}
	s.DisableDedup = true
	if groups, complete, err = s.ConsumedGroups(ctx, 1); err != nil || !complete || len(groups) > 0 {
		t.Fatal("dedup disabled must remain disabled")
	}
}
func TestRefillQueryPreservesRelevanceAndFilters(t *testing.T) {
	c := Context{Query: "software", retrievalAt: time.Now().UnixMilli(), Filters: Filters{ExcludeAuthors: []string{"2"}}}
	before, err := Query(c, Broadcast, "lexical", 80)
	if err != nil {
		t.Fatal(err)
	}
	c.excludeConsumedGroups = []int64{11}
	c.excludeRecalledIDs = []int64{12}
	after, err := Query(c, Broadcast, "lexical", 80)
	if err != nil {
		t.Fatal(err)
	}
	q := after["query"].(map[string]any)["bool"].(map[string]any)
	not := q["must_not"].([]any)
	if len(not) != 3 {
		t.Fatalf("missing exclusions: %v", not)
	}
	q["must_not"] = not[2:]
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatalf("refill changed matching or scoring: %s / %s", a, b)
	}
}

func TestConsumedRefillPrioritizesSeenHeadOutsideHistoryPrefix(t *testing.T) {
	e, s := refillFixture()
	s.docs = []Document{refillDoc(1)}
	s.seen["broadcast:1"] = true
	duplicate := refillDoc(2)
	duplicate.GroupID = 1
	s.pool = []Document{duplicate, refillDoc(3)}
	// The old consumed group is missing from the personal-set history prefix.
	x := runRefill(t, e, 20)
	if x.Refill == nil || x.Refill.Recalled != 1 || len(x.Candidates) != 1 || x.Candidates[0].Document.Ref.ID != 3 {
		t.Fatalf("seen head not excluded by group: %+v", x)
	}
}
func TestConsumedRefillSkipsUnmatchableBaseline(t *testing.T) {
	e, s := refillFixture()
	s.owner = OwnerContext{}
	x := runRefill(t, e, 20)
	if x.Refill != nil || s.historyCalls != 0 || s.refillCalls != 0 {
		t.Fatal("baseline should not read consumption history")
	}
}
