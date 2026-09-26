package discovery

import (
	"context"
	"eigenflux_server/pkg/need"
	searchindex "eigenflux_server/rpc/sort/discovery/index"

	"errors"
	"reflect"
	"strings"
	"testing"
)

type embedStub struct{ err error }

func (e embedStub) GetEmbedding(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, e.err
}
func vocabulary() *searchindex.Vocabulary {
	return &searchindex.Vocabulary{Version: "test1", Categories: []searchindex.Node{{ID: "design", Name: "Design"}}, Subtypes: []searchindex.Node{{ID: "web", Name: "Web", Category: "design"}}, Intents: []searchindex.Node{{ID: "landing", Name: "landing page", Category: "design", Subtype: "web", Vector: []float32{1, 0}}}}
}
func baseContext() Context {
	return Context{ID: 1, OwnerID: 10, State: "active", Kinds: AllKinds, Vector: []float32{1, 0}, TaxonomyVersion: "test1"}
}
func baseDoc(k Kind) Document {
	return Document{Ref: SourceRef{k, 20}, AuthorID: 20, Active: true, Visible: true, Text: "A landing page designer", Slots: searchindex.Slots{Category: "design", Subtype: "web", TaxonomyVersion: "test1", Lang: []string{"en"}}, Vector: []float32{1, 0}, Lexical: 10}
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
func TestCompilerConsumesDirectNeedWithoutInventedFilters(t *testing.T) {
	cc := Compiler{Taxonomy: vocabulary(), Embedder: embedStub{}, EmbeddingVersion: "test"}
	snapshot := capturedFixture(42, Agent)
	editCaptured(t, &snapshot, func(in *need.Input) {
		in.Target.Goal = "  landing page  "
		in.Target.Context = "design support"
		in.Constraints.Lang = []string{"EN"}
		in.Constraints.ProviderRegion = []string{"us"}
		in.Preferences = []need.Condition{{Text: "Prefer Chinese communication", SourceQuote: "prefer Chinese"}}
	})
	original := string(snapshot.Input)
	c, err := cc.Need(context.Background(), 10, 2, 1000, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if c.Filters.Category != "" || !reflect.DeepEqual(c.Filters.Lang, []string{"en"}) || !reflect.DeepEqual(c.Filters.ProviderRegion, []string{"US"}) || !reflect.DeepEqual(c.SoftIntents, []string{"landing"}) || c.NeedID() != 42 || c.Origin != "need_input" {
		t.Fatalf("wrong direct context: %+v", c)
	}
	if string(c.CapturedNeed.Input) != original || c.Query != "landing page  \ndesign support" || c.UnverifiedNeedReason != "" {
		t.Fatal("source or preferences reinterpreted", c)
	}
	cc.Embedder = embedStub{errors.New("down")}
	partial, err := cc.Need(context.Background(), 10, 3, 1000, snapshot)
	if err != nil || len(partial.Warnings) == 0 {
		t.Fatal("Need lost lexical fallback", err)
	}
	editCaptured(t, &snapshot, func(in *need.Input) { in.Constraints.DeadlineMS = num(900) })
	if _, err := cc.Need(context.Background(), 10, 4, 1000, snapshot); err == nil {
		t.Fatal("expired Need accepted")
	}
}

func TestOpenRequirementsPreservedWithoutBlockingRetrieval(t *testing.T) {
	cc := Compiler{Taxonomy: vocabulary(), Embedder: embedStub{}}
	snapshot := capturedFixture(42, Agent)
	editCaptured(t, &snapshot, func(in *need.Input) {
		in.Requirements = []need.Condition{{Text: "Do not upload production data", SourceQuote: "keep production data local"}}
	})
	c, err := cc.Need(context.Background(), 10, 2, 1000, snapshot)
	if err != nil || c.UnverifiedNeedReason != "" || Check(c, baseDoc(Agent), Search, 1000) != "" {
		t.Fatal(c, err)
	}
	if len(c.Vector) == 0 || len(c.Warnings) != 0 || string(c.CapturedNeed.Input) != string(snapshot.Input) {
		t.Fatal("requirements blocked embedding or changed source provenance", c)
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

func TestInlineNeedUsesCaptureJSONValidation(t *testing.T) {
	valid := `{"schema_version":"need_input.v2","intent_id":"42","intent_version":1,"need_type":"agent","target":{"goal":"designer"}}`
	if _, err := Decode[Request]([]byte(`{"need":` + valid + `}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(valid, `"need_type":"agent"`, `"need_type":"agent","need_type":"broadcast"`, 1),
		strings.Replace(valid, `"intent_id":"42"`, `"intent_id":"042"`, 1),
		strings.Replace(valid, `"target":`, `"priority":null,"target":`, 1),
		strings.Replace(valid, `"target":`, `"constraints":{"lang":null},"target":`, 1),
	} {
		if _, err := Decode[Request]([]byte(`{"need":` + bad + `}`)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestSearchMergePrioritizesExactWithinEachKind(t *testing.T) {
	makeCandidate := func(kind Kind, id int64, exact string, score float64) Candidate {
		return Candidate{Document: Document{Ref: SourceRef{kind, id}, ExactMatch: exact}, Score: Score{Value: score, Eligible: true}}
	}
	broadcast := makeCandidate(Broadcast, 1, "", .99)
	commission := makeCandidate(Commission, 2, "", .98)
	ordinaryAgent := makeCandidate(Agent, 3, "", .97)
	exactA := makeCandidate(Agent, 4, "name", .1)
	exactB := makeCandidate(Agent, 5, "name", .1)
	duplicate := makeCandidate(Agent, 4, "", .99)
	ineligible := makeCandidate(Agent, 6, "name", 1)
	ineligible.Score.Eligible = false
	in := []Candidate{broadcast, commission, duplicate, ordinaryAgent, exactB, ineligible, exactA, exactA}
	for _, kinds := range [][]Kind{AllKinds, {Commission, Broadcast, Agent}} {
		for _, limit := range []int{0, 1, 2, 3, 20} {
			got := Merge(in, kinds, Search, limit)
			want := []SourceRef{}
			if kinds[0] == Commission {
				want = append(want, commission.Document.Ref, broadcast.Document.Ref)
			} else {
				want = append(want, broadcast.Document.Ref, commission.Document.Ref)
			}
			want = append(want, exactA.Document.Ref, exactB.Document.Ref, ordinaryAgent.Document.Ref)
			if limit < len(want) {
				want = want[:limit]
			}
			refs := make([]SourceRef, len(got))
			for i, c := range got {
				refs[i] = c.Document.Ref
			}
			if !reflect.DeepEqual(refs, want) {
				t.Fatalf("kinds=%v limit=%d: got %v want %v", kinds, limit, refs, want)
			}
		}
	}
	if got := Merge(in, []Kind{Broadcast}, Search, 20); len(got) != 1 || got[0].Document.Ref != broadcast.Document.Ref {
		t.Fatal("exact match escaped requested kinds", got)
	}
}

func TestResultBlocksPreserveSelectionAndWithinKindOrder(t *testing.T) {
	in := []Candidate{}
	for i, kind := range []Kind{Agent, Broadcast, Commission, Agent, Broadcast, Commission} {
		in = append(in, Candidate{Document: Document{Ref: SourceRef{kind, int64(i + 1)}}, Context: Context{Priority: float64(10 - i)}, Score: Score{Eligible: true}})
	}
	// Selection still honors Need priority. Only the selected page is grouped.
	selected := Merge(in, AllKinds, Recommendation, 5)
	groupResultPages(selected, AllKinds, 5)
	want := []int64{2, 5, 3, 1, 4}
	for i, c := range selected {
		if c.Document.Ref.ID != want[i] {
			t.Fatal(selected)
		}
	}
	// Exact hits lead their own block even when their score is lower.
	in[3].Document.ExactMatch = "name"
	groupResultPages(in, []Kind{Commission, Agent, Broadcast}, len(in))
	want = []int64{3, 6, 4, 1, 2, 5}
	for i, c := range in {
		if c.Document.Ref.ID != want[i] {
			t.Fatal(in)
		}
	}
}
