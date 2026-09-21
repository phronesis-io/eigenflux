package discovery

import (
	"context"
	"eigenflux_server/pkg/taxonomy"
	"errors"
	"reflect"
	"testing"
)

type embedStub struct{ err error }

func (e embedStub) GetEmbedding(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, e.err
}
func vocabulary() *taxonomy.Vocabulary {
	return &taxonomy.Vocabulary{Version: "test1", Categories: []taxonomy.Node{{ID: "design", Name: "Design"}}, Subtypes: []taxonomy.Node{{ID: "web", Name: "Web", Category: "design"}}, Intents: []taxonomy.Node{{ID: "landing", Name: "landing page", Category: "design", Subtype: "web", Vector: []float32{1, 0}}}}
}
func baseContext() Context {
	return Context{ID: 1, OwnerID: 10, State: "active", Kinds: AllKinds, Vector: []float32{1, 0}, TaxonomyVersion: "test1"}
}
func baseDoc(k Kind) Document {
	return Document{Ref: SourceRef{k, 20}, AuthorID: 20, Active: true, Visible: true, Text: "A landing page designer", Slots: Slots{Category: "design", Subtype: "web", TaxonomyVersion: "test1", Lang: []string{"en"}}, Vector: []float32{1, 0}, Lexical: 10}
}
func num(n int64) *int64 { return &n }
func TestFilterAllKindsAndMissingEvidence(t *testing.T) {
	for _, k := range AllKinds {
		t.Run(string(k), func(t *testing.T) {
			c := baseContext()
			d := baseDoc(k)
			c.Filters = Filters{Category: "design", TaxonomyVersion: "test1", Lang: []string{"en"}, ProviderRegion: []string{"US"}}
			if got := Check(c, d, Search, 1000); got != "provider_region" {
				t.Fatalf("missing evidence passed: %s", got)
			}
			d.Slots.ProviderRegion = []string{"US"}
			if got := Check(c, d, Search, 1000); got != "" {
				t.Fatal(got)
			}
			d.Blocked = true
			if got := Check(c, d, Search, 1000); got != "blocked" {
				t.Fatal(got)
			}
		})
	}
}
func TestDeadlineDurationCurrencyAndZero(t *testing.T) {
	c := baseContext()
	c.Kinds = []Kind{Commission}
	c.Filters = Filters{BudgetMaxFen: num(0), Currency: "CNY", DeadlineMS: num(2000)}
	d := baseDoc(Commission)
	d.PriceFen = num(0)
	d.Currency = "CNY"
	d.DurationMS = num(1000)
	if got := Check(c, d, Search, 1000); got != "" {
		t.Fatal(got)
	}
	if got := Check(c, d, Search, 1001); got != "deadline" {
		t.Fatal(got)
	}
	d.DurationMS = num(1)
	d.Currency = "USD"
	if got := Check(c, d, Search, 1000); got != "budget" {
		t.Fatal(got)
	}
	d.Currency = "CNY"
	d.PriceFen = nil
	if got := Check(c, d, Search, 1000); got != "budget" {
		t.Fatal(got)
	}
}
func TestPeopleRelationshipDiffersByMode(t *testing.T) {
	c := baseContext()
	d := baseDoc(Agent)
	d.KnownContact = true
	if Check(c, d, Search, 1000) != "" || Check(c, d, Recommendation, 1000) != "known_contact" {
		t.Fatal("known contact semantics")
	}
	d.Blocked = true
	if Check(c, d, Search, 1000) != "blocked" {
		t.Fatal("query bypassed block")
	}
}
func TestExcludePhraseBoundaries(t *testing.T) {
	for _, tt := range []struct {
		text, term string
		want       bool
	}{{"partial ART", "art", true}, {"partial", "art", false}, {"ＡＲＴ studio", "art", true}, {"寻找设计服务", "设计", true}, {"modern design", "", false}} {
		if got := ContainsPhrase(tt.text, tt.term); got != tt.want {
			t.Fatalf("%q/%q: %v", tt.text, tt.term, got)
		}
	}
}
func TestInputModesAndTypedBounds(t *testing.T) {
	_, err := NormalizeRequest(Request{Query: "design", Filters: Filters{BudgetMaxFen: num(1), Currency: "CNY"}}, Search, 1000)
	if err == nil {
		t.Fatal("mixed kind budget accepted")
	}
	r, err := NormalizeRequest(Request{Query: "design"}, Search, 1000)
	if err != nil || r.Limit != 20 || len(r.SourceKinds) != 3 {
		t.Fatalf("raw query requires invented need: %+v %v", r, err)
	}
	_, err = NormalizeRequest(Request{Query: "query"}, Recommendation, 1000)
	if err == nil {
		t.Fatal("automatic query ambiguity")
	}
	_, err = Decode[Request]([]byte(`{"query":"x","agent_id":"12"}`))
	if err == nil {
		t.Fatal("owner injection")
	}
	_, err = Decode[Request]([]byte(`{"query":"x"} {}`))
	if err == nil {
		t.Fatal("trailing JSON")
	}
}
func TestCompilerNoInventedTargetAndAtomicEmbeddingFailure(t *testing.T) {
	cc := Compiler{Taxonomy: vocabulary(), Embedder: embedStub{}, EmbeddingVersion: "test"}
	c, err := cc.Query(context.Background(), 10, 1, 1000, Request{Query: "landing page", SourceKinds: []Kind{Broadcast}}, "query")
	if err != nil {
		t.Fatal(err)
	}
	if c.Filters.Category != "" || c.Need != nil || c.Origin != "query" {
		t.Fatalf("inferred hard target: %+v", c)
	}
	need := NeedInput{NeedType: "find_people", Priority: .8, Target: Target{Category: "design", Subtype: "web", FreeText: "landing page designer", ProposedIntents: []string{"landing page"}}, Outcome: "Find a collaborator"}
	n, err := cc.Need(context.Background(), 10, 2, 1000, need, []string{"zh"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Filters.Lang) != 0 || len(n.Filters.ProviderRegion) != 0 || n.Persistence != "saved" || !reflect.DeepEqual(n.SoftIntents, []string{"landing"}) {
		t.Fatalf("wrong defaults: %+v", n)
	}
	cc.Embedder = embedStub{errors.New("down")}
	if _, err := cc.Need(context.Background(), 10, 2, 1000, need, nil, true); err == nil {
		t.Fatal("incomplete saved Need accepted")
	}
	partial, err := cc.Query(context.Background(), 10, 3, 1000, Request{Query: "design", SourceKinds: []Kind{Agent}}, "query")
	if err != nil || len(partial.Warnings) == 0 {
		t.Fatal("raw query partial path not observable")
	}
}
func TestIndependentRulesAndMerge(t *testing.T) {
	rule := Rule{Version: "agent_rules_v3", BM25Scale: 1, CosineFloor: 0, MinRelevance: .4, Threshold: .4, HalfLifeMS: 1000}
	c := baseContext()
	a := Candidate{Context: c, Document: baseDoc(Agent)}
	a.Score = ScoreRules(c, a.Document, rule, 1000)
	if !a.Score.Eligible || a.Score.Version != "agent_rules_v3" {
		t.Fatal(a.Score)
	}
	b := a
	b.Document = baseDoc(Broadcast)
	b.Score = Score{Value: .99, Eligible: true, Version: "broadcast_rules_v7"}
	a.Score.Value = .5
	items := Merge([]Candidate{b, a, b}, []Kind{Agent, Broadcast}, Search, 20)
	if len(items) != 2 || items[0].Document.Ref.Type != Agent {
		t.Fatal("mixed scores compared or typed IDs collapsed")
	}
	c.Origin = "baseline"
	if ScoreRules(c, baseDoc(Agent), rule, 1000).Eligible {
		t.Fatal("people baseline enabled")
	}
}

func TestSavedNeedCannotSilentlyIgnoreTopLevelDefaults(t *testing.T) {
	_, err := NormalizeRequest(Request{NeedID: 1, Defaults: Defaults{Language: "card"}}, Search, 100)
	if err == nil {
		t.Fatal("top-level defaults would be ignored by saved Need")
	}
}

func TestPublicResponseHidesNumericDiagnosticsWithoutChangingInternalEnvelope(t *testing.T) {
	r := Response{Items: []ResultItem{PublicItem(Candidate{Document: baseDoc(Broadcast), Context: baseContext(), Score: Score{Value: .8, Version: "v1"}})}}
	public := PublicResponse(r)
	if _, ok := public.Items[0].Match["score"]; ok {
		t.Fatal("numeric score exposed")
	}
	if r.Items[0].Match["score"] != .8 || public.Items[0].Match["scorer_version"] != "v1" {
		t.Fatal("internal envelope mutated or provenance lost")
	}
}

func TestEveryMonetaryBoundRequiresCurrency(t *testing.T) {
	for _, f := range []Filters{{MinPriceFen: num(0)}, {MaxPriceFen: num(10)}, {BudgetMaxFen: num(10)}} {
		if ValidateFilters(f, []Kind{Commission}, 100) == nil {
			t.Fatal("unitless price accepted")
		}
		f.Currency = "CNY"
		if err := ValidateFilters(f, []Kind{Commission}, 100); err != nil {
			t.Fatal(err)
		}
	}
}
