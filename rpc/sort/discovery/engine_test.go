package discovery

import (
	"context"
	"eigenflux_server/pkg/need"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"encoding/json"

	"fmt"
	"sync"
	"testing"
)

type memStore struct {
	rows   map[int64]Context
	needs  map[int64]need.Snapshot
	active []need.Snapshot
}

func (s *memStore) Current(_ context.Context, owner, id int64) (need.Snapshot, error) {
	n, ok := s.needs[id]
	if !ok || owner != 1 {
		return need.Snapshot{}, need.ErrNotFound
	}
	return n, nil
}
func (s *memStore) CheckIntent(context.Context, int64, int64, int64) error { return nil }
func (s *memStore) Create(_ context.Context, c Context) (Context, error) {
	s.rows[c.ID] = c
	return c, nil
}
func (s *memStore) Active(context.Context, int64, []string, int64) ([]need.Snapshot, error) {
	return s.active, nil
}
func capturedFixture(id int64, kind Kind) need.Snapshot {
	in := need.Input{SchemaVersion: need.InputSchemaVersion, IntentID: 40, IntentVersion: 2, NeedType: string(kind), Target: need.Target{Goal: "design"}}
	raw, _ := json.Marshal(in)
	return need.Snapshot{InputID: id, IntentID: 40, IntentVersion: 2, Input: raw}
}
func editCaptured(t *testing.T, s *need.Snapshot, edit func(*need.Input)) {
	t.Helper()
	in, err := s.ExecutionInput()
	if err != nil {
		t.Fatal(err)
	}
	edit(&in)
	s.Input, err = json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
}

type countIDs int64

func (i *countIDs) NextID() (int64, error) { *i++; return int64(*i), nil }

type sourceFake struct {
	mu           sync.Mutex
	docs         []Document
	owner        OwnerContext
	seen         map[string]bool
	seenCalls    int
	hydrateCalls int
	onHydrate    func()
	fail         bool
	exact        []Document
}

func (s *sourceFake) Owner(context.Context, int64) (OwnerContext, error) { return s.owner, nil }
func (s *sourceFake) Recall(_ context.Context, _ Context, k Kind, ch string, _ int) ([]Document, error) {
	if s.fail {
		return nil, fmt.Errorf("offline")
	}
	if ch == "exact" {
		return s.exact, nil
	}
	out := []Document{}
	for _, d := range s.docs {
		if d.Ref.Type == k {
			out = append(out, d)
		}
	}
	return out, nil
}
func (s *sourceFake) Hydrate(_ context.Context, _ int64, _ Mode, docs []Document) ([]Document, error) {
	s.hydrateCalls++
	if s.onHydrate != nil {
		s.onHydrate()
	}
	return docs, nil
}
func (s *sourceFake) Seen(context.Context, int64, []Document) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seenCalls++
	return s.seen, nil
}
func engineFixture() (*Engine, *sourceFake, *memStore) {
	s := &sourceFake{seen: map[string]bool{}}
	store := &memStore{rows: map[int64]Context{}, needs: map[int64]need.Snapshot{}}
	ids := countIDs(100)
	rules := Rules{}
	for _, k := range AllKinds {
		rules[k] = map[Mode]Rule{}
		for _, m := range []Mode{Search, Recommendation} {
			rules[k][m] = Rule{Version: "rules", BM25Scale: 1, CosineFloor: 0, MinRelevance: .1, Threshold: .1, HalfLifeMS: 1000}
		}
	}
	e := &Engine{Compiler: &Compiler{Taxonomy: &searchindex.Vocabulary{Version: "v1", Categories: []searchindex.Node{{ID: "design", Name: "Design"}}}}, Store: store, Needs: store, IDs: &ids, Sources: s, Rules: rules}
	return e, s, store
}

type unexpectedEmbedding struct{ t *testing.T }

func (e unexpectedEmbedding) GetEmbedding(context.Context, string) ([]float32, error) {
	e.t.Fatal("exact Agent lookup must not call embedding")
	return nil, nil
}

func TestExactAgentLookupBypassesSemanticGatesButKeepsFilters(t *testing.T) {
	for _, match := range []string{"agent_id", "short_id", "name"} {
		t.Run(match, func(t *testing.T) {
			e, s, _ := engineFixture()
			e.Compiler.Embedder = unexpectedEmbedding{t}
			e.Rules[Agent][Search] = Rule{Version: "strict", BM25Scale: 1, MinRelevance: 1, Threshold: 1, HalfLifeMS: 1000}
			d := Document{Ref: SourceRef{Agent, 20}, AuthorID: 20, Version: "1", Active: true, Visible: true, ExactMatch: match}
			s.exact = []Document{d}
			s.docs = []Document{{Ref: SourceRef{Agent, 30}, AuthorID: 30, Active: true, Visible: true, Lexical: 100}}
			r := Request{Query: "some identity", SourceKinds: []Kind{Agent}}
			x, err := e.Execute(context.Background(), 1, r, Search, 100)
			if err != nil || len(x.Candidates) != 1 || x.Candidates[0].Document.Ref.ID != 20 {
				t.Fatalf("exact lookup: %+v, %v", x, err)
			}
			if x.Candidates[0].Score.Kind != "exact_match" || len(x.PartialReasons) != 0 {
				t.Fatalf("exact result attribution: %+v", x)
			}
			for _, reason := range []string{"blocked", "inactive", "self", "language"} {
				candidate := d
				r.Filters = Filters{}
				switch reason {
				case "blocked":
					candidate.Blocked = true
				case "inactive":
					candidate.Active = false
				case "self":
					candidate.AuthorID = 1
				case "language":
					r.Filters.Lang = []string{"en"}
				}
				s.exact = []Document{candidate}
				x, err = e.Execute(context.Background(), 1, r, Search, 100)
				if err != nil || len(x.Candidates) != 0 || x.FallbackReason != "" {
					t.Fatalf("exact lookup bypassed %s: %+v, %v", reason, x, err)
				}
			}
		})
	}
}

func TestUnknownNumericAgentDoesNotFallThroughToSemanticRetrieval(t *testing.T) {
	e, s, _ := engineFixture()
	e.Compiler.Embedder = unexpectedEmbedding{t}
	s.docs = []Document{{Ref: SourceRef{Agent, 20}, AuthorID: 20, Active: true, Visible: true, Lexical: 100}}
	for _, query := range []string{"9007199254740993", "99999999999999999999999", "0"} {
		x, err := e.Execute(context.Background(), 1, Request{Query: query, SourceKinds: []Kind{Agent}}, Search, 100)
		if err != nil || len(x.Candidates) != 0 || x.Status != "no_match" {
			t.Fatalf("unknown identity returned a fuzzy candidate: %+v, %v", x, err)
		}
	}
}

func TestEngineQueryRepeatableAndTypedIDs(t *testing.T) {
	e, s, _ := engineFixture()
	for _, k := range AllKinds {
		s.docs = append(s.docs, Document{Ref: SourceRef{k, 9}, AuthorID: 2, Version: "1", Text: "design", Active: true, Visible: true, Lexical: 10})
	}
	s.seen["broadcast:9"] = true
	for i := 0; i < 2; i++ {
		x, err := e.Execute(context.Background(), 1, Request{Query: "design"}, Search, 100)
		if err != nil || len(x.Candidates) != 3 {
			t.Fatalf("%+v %v", x, err)
		}
	}
	if s.seenCalls != 0 {
		t.Fatal("query reads automatic history")
	}
}
func TestEngineBaselineAndNoBroadening(t *testing.T) {
	e, s, store := engineFixture()
	s.docs = []Document{{Ref: SourceRef{Broadcast, 9}, AuthorID: 2, Version: "1", Quality: 1, Active: true, Visible: true}}
	x, err := e.Execute(context.Background(), 1, Request{}, Recommendation, 100)
	if err != nil || len(x.Candidates) != 1 || x.Contexts[0].Origin != "baseline" {
		t.Fatalf("%+v %v", x, err)
	}
	x, err = e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Agent}}, Recommendation, 100)
	if err != nil || x.Status != "insufficient_context" {
		t.Fatal(x, err)
	}
	n := capturedFixture(4, Broadcast)
	editCaptured(t, &n, func(in *need.Input) { in.Constraints.Lang = []string{"zh"} })
	store.active = []need.Snapshot{n}
	store.needs[4] = n
	x, err = e.Execute(context.Background(), 1, Request{}, Recommendation, 100)
	if err != nil || len(x.Candidates) != 0 || x.FallbackReason != "" {
		t.Fatal("constrained no-match broadened", x, err)
	}
}
func TestEngineScopesAuthorityAndFailures(t *testing.T) {
	e, s, store := engineFixture()
	store.needs[7] = capturedFixture(7, Commission)
	if _, err := e.Execute(context.Background(), 2, Request{NeedID: 7}, Search, 100); err == nil {
		t.Fatal("cross owner")
	}
	if _, err := e.Execute(context.Background(), 1, Request{NeedID: 7, SourceKinds: []Kind{Broadcast}}, Search, 100); err == nil {
		t.Fatal("kind bypass")
	}
	s.fail = true
	if _, err := e.Execute(context.Background(), 1, Request{Query: "design"}, Search, 100); err == nil {
		t.Fatal("retrieval failure became no match")
	}
}

func TestEmptyStatusesRemainDistinct(t *testing.T) {
	e, s, _ := engineFixture()
	request := Request{Query: "design", SourceKinds: []Kind{Broadcast}}
	x, err := e.Execute(context.Background(), 1, request, Search, 100)
	if err != nil || x.Status != "no_match" {
		t.Fatal(x, err)
	}
	s.docs = []Document{{Ref: SourceRef{Broadcast, 9}, AuthorID: 2, Version: "1", Active: true, Visible: true}}
	x, err = e.Execute(context.Background(), 1, request, Search, 100)
	if err != nil || x.Status != "below_threshold" {
		t.Fatal(x, err)
	}
	s.seen["broadcast:9"] = true
	x, err = e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}}, Recommendation, 100)
	if err != nil || x.Status != "exhausted" {
		t.Fatal(x, err)
	}
}

func TestNeedSnapshotIsNotRecheckedAfterHydration(t *testing.T) {
	e, source, store := engineFixture()
	store.needs[7] = capturedFixture(7, Broadcast)
	source.docs = []Document{{Ref: SourceRef{Broadcast, 9}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10}}
	source.onHydrate = func() { delete(store.needs, 7) }
	x, err := e.Execute(context.Background(), 1, Request{NeedID: 7}, Search, 100)
	if err != nil || len(x.Candidates) != 1 || x.Candidates[0].Context.Revision != 1 || source.hydrateCalls != 1 {
		t.Fatal(x, err, source.hydrateCalls)
	}
	if _, err := e.Execute(context.Background(), 1, Request{NeedID: 7}, Search, 100); err == nil {
		t.Fatal("next request accepted a closed Need")
	}
}

func TestRecommendationIntersectsCapturedNeedWithoutChangingProvenance(t *testing.T) {
	e, source, store := engineFixture()
	snapshot := capturedFixture(7, Commission)
	editCaptured(t, &snapshot, func(in *need.Input) { in.Constraints.BudgetMaxFen = num(100); in.Constraints.Currency = "CNY" })
	store.needs[7] = snapshot
	source.docs = []Document{{Ref: SourceRef{Commission, 9}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10, PriceFen: num(60), Currency: "CNY"}}
	request := Request{NeedIDs: []string{"7"}, SourceKinds: []Kind{Commission}, Filters: Filters{MaxPriceFen: num(50), Currency: "CNY"}}
	x, err := e.Execute(context.Background(), 1, request, Recommendation, 100)
	if err != nil || len(x.Candidates) != 0 || len(x.Contexts) != 1 || x.FallbackReason != "" {
		t.Fatal(x, err)
	}
	c := x.Contexts[0]
	if *c.Filters.MaxPriceFen != 50 || *c.Filters.BudgetMaxFen != 100 || c.NeedID() != 7 || c.SourceNeedRevision != 2 || c.CapturedNeed.InputID != snapshot.InputID {
		t.Fatalf("intersection lost provenance: %+v", c)
	}
	if string(c.CapturedNeed.Input) != string(snapshot.Input) || c.SpecHash != hashContext(c) {
		t.Fatal("source was rewritten or hash did not freeze intersection")
	}
}

func TestRecommendationLimitIsACeiling(t *testing.T) {
	e, source, store := engineFixture()
	for _, kind := range AllKinds {
		source.docs = append(source.docs, Document{Ref: SourceRef{kind, 9}, AuthorID: 2, Version: "1", Text: "design", Active: true, Visible: true, Lexical: 10})
		store.active = append(store.active, capturedFixture(int64(len(store.active)+1), kind))
	}
	for _, tc := range []struct{ limit, want int }{{0, 3}, {1, 1}, {2, 2}, {5, 3}, {100, 3}} {
		x, err := e.Execute(context.Background(), 1, Request{Limit: tc.limit}, Recommendation, 100)
		if err != nil || len(x.Candidates) != tc.want {
			t.Fatalf("limit=%d got=%d err=%v", tc.limit, len(x.Candidates), err)
		}
	}
	source.docs[0].Active = false
	source.docs[1].Blocked = true
	x, err := e.Execute(context.Background(), 1, Request{Limit: 20}, Recommendation, 100)
	if err != nil || len(x.Candidates) != 1 || x.FallbackReason != "" {
		t.Fatal("limit relaxed eligibility", x, err)
	}
	for _, limit := range []int{-1, 101} {
		if _, err := e.Execute(context.Background(), 1, Request{Limit: limit}, Recommendation, 100); err == nil {
			t.Fatal("accepted limit", limit)
		}
	}
}

func TestSearchPrefetchKeepsCandidatesBeyondFirstPage(t *testing.T) {
	e, source, _ := engineFixture()
	for id := int64(1); id <= 8; id++ {
		source.docs = append(source.docs, Document{Ref: SourceRef{Agent, id + 10}, AuthorID: 2, Version: "1", Text: "design", Active: true, Visible: true, Lexical: 10})
	}
	service := Service{Engine: e}
	value, err := service.Run(context.Background(), 1, Operation{Name: "search_prefetch", Payload: `{"query":"design","source_kinds":["agent"],"limit":2}`}, 100)
	if err != nil {
		t.Fatal(err)
	}
	x := value.(Execution)
	if len(x.Candidates) != 8 || x.Mode != Search {
		t.Fatal(x)
	}
}

func TestEmptyKindsDoNotBlockMerge(t *testing.T) {
	for _, mode := range []Mode{Search, Recommendation} {
		for mask := 0; mask < 8; mask++ {
			t.Run(fmt.Sprintf("%s/kinds-%d", mode, mask), func(t *testing.T) {
				e, source, _ := engineFixture()
				source.owner.Clauses = []string{"design"}
				want := map[Kind]bool{}
				for i, kind := range AllKinds {
					if mask&(1<<i) != 0 {
						source.docs = append(source.docs, Document{Ref: SourceRef{kind, 9}, AuthorID: 2, Version: "1", Text: "design", Active: true, Visible: true, Lexical: 10})
						want[kind] = true
					}
				}
				request := Request{SourceKinds: AllKinds, Limit: 20}
				if mode == Search {
					request.Query = "design"
				}
				x, err := e.Execute(context.Background(), 1, request, mode, 100)
				if err != nil || len(x.Candidates) != len(want) {
					t.Fatal("empty route blocked merge", x, err)
				}
				for _, candidate := range x.Candidates {
					if !want[candidate.Document.Ref.Type] {
						t.Fatal(candidate)
					}
				}
				if mask == 0 && (x.Status != "no_match" || x.Candidates == nil) {
					t.Fatal("all-empty must remain a successful empty result", x)
				}
			})
		}
	}
}

func TestUnverifiedNeedDoesNotBlockOtherNeeds(t *testing.T) {
	e, source, store := engineFixture()
	blocked := capturedFixture(7, Commission)
	editCaptured(t, &blocked, func(in *need.Input) { in.Requirements = []need.Condition{{Text: "Do not upload production data"}} })
	valid := capturedFixture(8, Agent)
	source.docs = []Document{{Ref: SourceRef{Agent, 9}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10}}
	store.active = []need.Snapshot{blocked, valid}
	x, err := e.Execute(context.Background(), 1, Request{}, Recommendation, 100)
	if err != nil || len(x.Candidates) != 1 || x.Candidates[0].Context.NeedID() != 8 {
		t.Fatal(x, err)
	}
	source.fail = true
	if _, err = e.Execute(context.Background(), 1, Request{}, Recommendation, 100); err == nil {
		t.Fatal("unverified Need masked unavailable retrieval")
	}
	source.fail = false

	store.active = []need.Snapshot{blocked}
	x, err = e.Execute(context.Background(), 1, Request{}, Recommendation, 100)
	if err != nil || x.Status != "no_match" || len(x.Candidates) != 0 || x.FallbackReason != "" {
		t.Fatal(x, err)
	}
}
