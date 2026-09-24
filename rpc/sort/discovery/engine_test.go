package discovery

import (
	"context"
	searchindex "eigenflux_server/rpc/sort/discovery/index"

	"fmt"
	"sync"
	"testing"
)

type memStore struct {
	rows   map[int64]Context
	active []Context
}

func (s *memStore) Get(_ context.Context, owner, id int64, _ bool) (Context, error) {
	c, ok := s.rows[id]
	if !ok || c.OwnerID != owner {
		return Context{}, Failure(404, "need_not_found")
	}
	return c, nil
}
func (s *memStore) Create(_ context.Context, c Context, _ string) (Context, error) {
	s.rows[c.ID] = c
	return c, nil
}
func (s *memStore) Active(context.Context, int64, []Kind, int64) ([]Context, error) {
	return s.active, nil
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
	store := &memStore{rows: map[int64]Context{}}
	ids := countIDs(100)
	rules := Rules{}
	for _, k := range AllKinds {
		rules[k] = map[Mode]Rule{}
		for _, m := range []Mode{Search, Recommendation} {
			rules[k][m] = Rule{Version: "rules", BM25Scale: 1, CosineFloor: 0, MinRelevance: .1, Threshold: .1, HalfLifeMS: 1000}
		}
	}
	e := &Engine{Compiler: &Compiler{Taxonomy: &searchindex.Vocabulary{Version: "v1", Categories: []searchindex.Node{{ID: "design", Name: "Design"}}}}, Store: store, IDs: &ids, Sources: s, Rules: rules}
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
	need := Context{ID: 4, OwnerID: 1, State: "active", Persistence: "saved", Origin: "saved_need", Kinds: []Kind{Broadcast}, TaxonomyVersion: "v1", Filters: Filters{Category: "design", TaxonomyVersion: "v1"}}
	store.active = []Context{need}
	store.rows[4] = need
	x, err = e.Execute(context.Background(), 1, Request{}, Recommendation, 100)
	if err != nil || len(x.Candidates) != 0 || x.FallbackReason != "" {
		t.Fatal("constrained no-match broadened", x, err)
	}
}
func TestEngineScopesAuthorityAndFailures(t *testing.T) {
	e, s, store := engineFixture()
	store.rows[7] = Context{ID: 7, OwnerID: 1, State: "active", Kinds: []Kind{Commission}, Persistence: "saved", TaxonomyVersion: "v1"}
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
	need := Context{ID: 7, OwnerID: 1, State: "active", Revision: 1, Kinds: []Kind{Broadcast}, Persistence: "saved", TaxonomyVersion: "v1"}
	store.rows[7] = need
	source.docs = []Document{{Ref: SourceRef{Broadcast, 9}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10}}
	source.onHydrate = func() { closed := need; closed.State = "closed"; closed.Revision++; store.rows[7] = closed }
	x, err := e.Execute(context.Background(), 1, Request{NeedID: 7}, Search, 100)
	if err != nil || len(x.Candidates) != 1 || x.Candidates[0].Context.Revision != 1 || source.hydrateCalls != 1 {
		t.Fatal(x, err, source.hydrateCalls)
	}
	if _, err := e.Execute(context.Background(), 1, Request{NeedID: 7}, Search, 100); err == nil {
		t.Fatal("next request accepted a closed Need")
	}
}
