package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"eigenflux_server/pkg/need"
	"eigenflux_server/rpc/sort/discoverylr"
)

// ratingFake scores by one input (cos_best0 by default) so tests can predict
// the order.
type ratingFake struct {
	calls    int
	fail     bool
	by       string
	contract string
}

var cosBest = slices.Index(discoverylr.RatingFeatureNames, "cos_best0")

func (f *ratingFake) ScoreBatch(v [][]float64) ([]discoverylr.Result, bool) {
	f.calls++
	if f.fail {
		return nil, false
	}
	at := cosBest
	if f.by != "" {
		at = slices.Index(discoverylr.RatingV2FeatureNames, f.by)
	}
	contract := f.contract
	if contract == "" {
		contract = discoverylr.RatingContract
	}
	out := make([]discoverylr.Result, len(v))
	for i, x := range v {
		out[i] = discoverylr.Result{ModelVersion: "rating-test", Contract: contract, Probability: .1 + .8*x[at]}
	}
	return out, true
}

type pgcFake struct {
	calls   int
	authors [][]int64
	err     error
}

func (f *pgcFake) classify(_ context.Context, ids []int64) (map[int64]bool, error) {
	f.calls++
	f.authors = append(f.authors, append([]int64(nil), ids...))
	if f.err != nil {
		return nil, f.err
	}
	out := map[int64]bool{}
	for _, id := range ids {
		out[id] = id == 77
	}
	return out, nil
}

func ratingFixture() (*Engine, *profileFake, *memStore, *ratingFake, *learnedFake, *pgcFake) {
	e, s, store := profileFixtureWithStore()
	e.ProfileSemanticBPS = 0
	rating, learned, pgc := &ratingFake{}, &learnedFake{}, &pgcFake{}
	e.RatingLearned, e.Learned, e.PGCAuthors, e.RatingLRBPS = rating, learned, pgc.classify, 10000
	return e, s, store, rating, learned, pgc
}

func feature(c Candidate, name string) float64 {
	return c.RatingFeatures[slices.Index(discoverylr.RatingFeatureNames, name)]
}

func TestRatingVectorMatchesPythonFixture(t *testing.T) {
	raw, err := os.ReadFile("../discoverylr/testdata/discovery_rating_lr_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Model json.RawMessage `json:"model"`
		Cases []struct {
			Candidate   Candidate        `json:"candidate"`
			Similarity  ratingSimilarity `json:"similarity"`
			PGC         bool             `json:"pgc"`
			Features    []float64        `json:"features"`
			Probability float64          `json:"probability"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "model.json")
	if err = os.WriteFile(path, data.Model, 0600); err != nil {
		t.Fatal(err)
	}
	model := discoverylr.NewRating(true, path, time.Hour)
	defer model.Close()
	if len(data.Cases) < 40 {
		t.Fatalf("fixture too small: %d", len(data.Cases))
	}
	for i, tc := range data.Cases {
		got, ok := ratingVector(tc.Candidate, tc.Similarity, tc.Candidate.Document.Channels, tc.PGC)
		if !ok || len(got) != len(tc.Features) {
			t.Fatalf("case %d: ok=%v len=%d", i, ok, len(got))
		}
		for j := range got {
			if math.Abs(got[j]-tc.Features[j]) > 1e-12 {
				t.Fatalf("case %d %s: got %v want %v", i, discoverylr.RatingFeatureNames[j], got[j], tc.Features[j])
			}
		}
		scores, ok := model.ScoreBatch([][]float64{got})
		if !ok || math.Abs(scores[0].Probability-tc.Probability) > 1e-9 || scores[0].Contract != discoverylr.RatingContract {
			t.Fatalf("case %d: score %+v ok=%v want %v", i, scores, ok, tc.Probability)
		}
	}
}

func TestRatingRanksNeedCandidatesWithCrossContextSimilarity(t *testing.T) {
	e, s, store, rating, learned, pgc := ratingFixture()
	store.active = []need.Snapshot{capturedFixture(9, Broadcast)}
	e.Compiler.NeedVectors = cacheLookupFunc(func(context.Context, int64, string, string) ([]float32, error) {
		return testNeedVector, nil
	})
	s.docs = []Document{profileDoc(1, 5, 1), profileDoc(2, 5, 1), profileDoc(3, 5, 1), profileDoc(4, 5, 1)}
	s.docs[1].AuthorID = 77
	for id, cos := range map[int64]float64{1: .1, 2: .3, 3: .7} {
		s.needCosine[id] = cos
	}
	for id, cos := range map[int64]float64{1: .2, 2: .6, 3: .4} {
		s.cosine[id] = cos
	}
	x := runProfile(t, e)
	if got := fmt.Sprint(ids(x)); got != "[3 2 1 4]" {
		t.Fatalf("got %s", got)
	}
	if rating.calls != 1 || learned.calls != 0 || pgc.calls != 1 || s.vectorCalls != 1 || s.cosineCalls != 2 {
		t.Fatalf("rating=%d learned=%d pgc=%d vector=%d cosine=%d", rating.calls, learned.calls, pgc.calls, s.vectorCalls, s.cosineCalls)
	}
	for _, c := range x.Candidates {
		if !ratingRanked(c) || len(c.RatingFeatures) != len(discoverylr.RatingFeatureNames) || feature(c, "o_need") != 1 {
			t.Fatalf("missing rating attribution: %+v", c)
		}
	}
	top := x.Candidates[0]
	want := map[string]float64{"need_cos_ctx0": .7, "need_cos_max0": .7, "prof_cos0": .4, "cos_best0": .7, "has_need": 1, "has_prof": 1, "n_needs01": .1, "need_cos_max_h45": .7 - .45, "is_pgc": 0}
	for name, v := range want {
		if math.Abs(feature(top, name)-v) > 1e-12 {
			t.Fatalf("%s = %v, want %v", name, feature(top, name), v)
		}
	}
	if feature(x.Candidates[1], "is_pgc") != 1 {
		t.Fatal("author classification not applied")
	}
	if last := x.Candidates[3]; feature(last, "has_need") != 0 || feature(last, "has_prof") != 0 || feature(last, "n_needs01") != 0 {
		t.Fatalf("an item without an embedding has no similarity: %v", last.RatingFeatures)
	}
	if item := PublicItem(top); item.Match["score_kind"] != "positive_rating_probability" || item.Match["scorer_version"] != "rating-test" {
		t.Fatalf("public attribution: %+v", item.Match)
	}
}

func TestRatingRanksProfileClausesWithoutNeeds(t *testing.T) {
	e, s, _, rating, _, _ := ratingFixture()
	s.docs = []Document{profileDoc(1, 5, 1), profileDoc(2, 5, 1), profileDoc(3, 5, 1), profileDoc(4, 5, 1)}
	for id, cos := range map[int64]float64{1: .2, 2: .6, 3: .4} {
		s.cosine[id] = cos
	}
	x := runProfile(t, e)
	if got := fmt.Sprint(ids(x)); got != "[2 3 1 4]" || rating.calls != 1 {
		t.Fatalf("got %s calls=%d", got, rating.calls)
	}
	for _, c := range x.Candidates {
		if feature(c, "o_profile") != 1 || feature(c, "has_need") != 0 || feature(c, "need_cos_max0") != 0 {
			t.Fatalf("unexpected Need evidence: %v", c.RatingFeatures)
		}
	}
	if s.cosineCalls != 1 || len(s.cosineIDs[0]) != 4 {
		t.Fatalf("one profile similarity read for all candidates: %v", s.cosineIDs)
	}
}

func TestRatingFallsBackToExistingRanking(t *testing.T) {
	for name, mutate := range map[string]func(*Engine, *profileFake, *ratingFake, *pgcFake){
		"cosine error":  func(_ *Engine, s *profileFake, _ *ratingFake, _ *pgcFake) { s.cosineErr = fmt.Errorf("es down") },
		"vector error":  func(_ *Engine, s *profileFake, _ *ratingFake, _ *pgcFake) { s.vectorErr = fmt.Errorf("db down") },
		"author error":  func(_ *Engine, _ *profileFake, _ *ratingFake, p *pgcFake) { p.err = fmt.Errorf("db down") },
		"no model":      func(_ *Engine, _ *profileFake, r *ratingFake, _ *pgcFake) { r.fail = true },
		"no classifier": func(e *Engine, _ *profileFake, _ *ratingFake, _ *pgcFake) { e.PGCAuthors = nil },
	} {
		t.Run(name, func(t *testing.T) {
			e, s, _, rating, learned, pgc := ratingFixture()
			s.docs = []Document{profileDoc(1, 5, 1), profileDoc(2, 5, 1)}
			s.cosine[1], s.cosine[2] = .2, .6
			mutate(e, s, rating, pgc)
			x := runProfile(t, e)
			if len(x.Candidates) != 2 || learned.calls == 0 {
				t.Fatalf("existing ranking must apply: %+v", x.Candidates)
			}
			for _, c := range x.Candidates {
				if ratingRanked(c) || c.RatingFeatures != nil || c.LR == nil || c.LR.Contract != discoverylr.Contract {
					t.Fatalf("mixed scorers: %+v", c)
				}
			}
			if len(x.PartialReasons) != 0 {
				t.Fatalf("ranking fallback is not a partial result: %v", x.PartialReasons)
			}
		})
	}
}

func TestRatingOffAndOutOfScopeRequestsAreUntouched(t *testing.T) {
	e, s, _, rating, learned, pgc := ratingFixture()
	s.docs = []Document{profileDoc(1, 5, 1)}
	e.RatingLRBPS = 0
	runProfile(t, e)
	if rating.calls != 0 || pgc.calls != 0 || s.vectorCalls != 0 || s.cosineCalls != 0 || learned.calls == 0 {
		t.Fatalf("off path read rating evidence: rating=%d pgc=%d vector=%d cosine=%d", rating.calls, pgc.calls, s.vectorCalls, s.cosineCalls)
	}
	e.RatingLRBPS = 10000
	if _, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast, Commission}, Limit: 20}, Recommendation, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), 1, Request{Query: "battery", SourceKinds: []Kind{Broadcast}, Limit: 20}, Search, testNow); err != nil {
		t.Fatal(err)
	}
	if rating.calls != 0 {
		t.Fatal("rating model scored a multi-kind recommendation or a search")
	}
}

func TestRatingNeedBudget(t *testing.T) {
	state := &ratingRequest{}
	contexts := []Context{}
	for i := 0; i <= ratingMaxNeeds; i++ {
		contexts = append(contexts, Context{ID: int64(i + 1), CapturedNeed: &need.Snapshot{}, Vector: []float32{float32(i), 1}, Kinds: []Kind{Broadcast}})
	}
	if err := state.start(contexts, nil); err == nil {
		t.Fatal("accepted more Need vectors than the read budget")
	}
	duplicate := []Context{contexts[0], contexts[0], {ID: 99, CapturedNeed: &need.Snapshot{}, Vector: contexts[0].Vector, Kinds: []Kind{Broadcast}}}
	state = &ratingRequest{}
	if err := state.start(duplicate, nil); err != nil || len(state.needs) != 1 || state.needOf[99] != 0 {
		t.Fatalf("identical Need vectors must count once: %v %d", err, len(state.needs))
	}
}

func TestRatingRefillReadsOnlyNewEvidence(t *testing.T) {
	e, s, _, _, _, pgc := ratingFixture()
	s.cosine[1], s.cosine[2], s.cosine[3] = .2, .6, .4
	contexts := []Context{{ID: 1, Origin: "agent_context", Kinds: []Kind{Broadcast}}}
	candidate := func(id int64) Candidate {
		return Candidate{Context: contexts[0], Document: profileDoc(id, 5, 1), Score: Score{Eligible: true, Value: .5, Relevance: .5, RequestTime: testNow,
			Features: map[string]float64{"lexical": .5, "semantic": 0, "freshness": 1, "quality": .5}}}
	}
	state := &ratingRequest{}
	first := []Candidate{candidate(1), candidate(2)}
	if !e.rankRating(context.Background(), 1, []Kind{Broadcast}, contexts, first, Recommendation, nil, state) {
		t.Fatal("first selection not scored")
	}
	second := []Candidate{candidate(1), candidate(2), candidate(3)}
	if !e.rankRating(context.Background(), 1, []Kind{Broadcast}, contexts, second, Recommendation, nil, state) {
		t.Fatal("refill selection not scored")
	}
	if s.vectorCalls != 1 || fmt.Sprint(s.cosineIDs) != "[[1 2] [3]]" || pgc.calls != 1 {
		t.Fatalf("evidence re-read: vector=%d cosine=%v pgc=%d", s.vectorCalls, s.cosineIDs, pgc.calls)
	}
	if feature(second[2], "prof_cos0") != .4 {
		t.Fatalf("refill similarity: %v", second[2].RatingFeatures)
	}
}

func TestRatingBucketIsIndependent(t *testing.T) {
	agreeSemantic, agreeRefill, n := 0, 0, 2000
	for owner := int64(1); owner <= int64(n); owner++ {
		if ratingEnabled(owner, 5000) == profileSemanticEnabled(owner, 5000) {
			agreeSemantic++
		}
		if ratingEnabled(owner, 5000) == consumedRefillEnabled(owner, 5000) {
			agreeRefill++
		}
	}
	for _, agree := range []int{agreeSemantic, agreeRefill} {
		if agree < n*4/10 || agree > n*6/10 {
			t.Fatalf("buckets correlated: %d/%d agree", agree, n)
		}
	}
	if ratingEnabled(1, 0) || !ratingEnabled(1, 10000) {
		t.Fatal("bounds")
	}
}

func TestMergeOrdersRatingAcrossContextsAndKeepsInjectedFront(t *testing.T) {
	rated := func(id, ctx int64, order int, score float64, reasons ...string) Candidate {
		c := Candidate{Context: Context{ID: ctx, Origin: "agent_context"}, Document: Document{Ref: SourceRef{Broadcast, id}},
			Score: Score{Eligible: true}, Order: order, FinalScore: score, Reasons: reasons,
			LR: &discoverylr.Result{Contract: discoverylr.RatingContract, ModelVersion: "m", Probability: score}}
		return c
	}
	needCandidate := rated(9, 3, 1, .05)
	needCandidate.Context.CapturedNeed = &need.Snapshot{}
	in := []Candidate{
		rated(1, 1, 1, .2, "inject:0"), rated(2, 1, 2, .5), rated(3, 1, 3, .1),
		rated(4, 2, 1, .9), rated(5, 2, 2, .3),
		needCandidate,
	}
	if got := fmt.Sprint(ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 10, false, nil)})); got != "[9 4 1 2 5 3]" {
		t.Fatalf("got %s", got)
	}
	slices.Reverse(in)
	if got := fmt.Sprint(ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 10, false, nil)})); got != "[9 4 1 2 5 3]" {
		t.Fatalf("order depends on input order: %s", got)
	}
	in[0].Context.Priority = 1
	in[0].Context.CapturedNeed = nil
	if got := fmt.Sprint(ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 10, false, nil)})); got != "[9 4 1 2 5 3]" {
		t.Fatalf("priority: %s", got)
	}
}

func TestRatingVectorBoundsSimilarity(t *testing.T) {
	c := Candidate{Context: Context{Origin: "agent_context"}, Document: profileDoc(1, 5, 1), Score: Score{Eligible: true, Value: .5, Relevance: .5, RequestTime: testNow,
		Features: map[string]float64{"lexical": .5, "semantic": 0, "freshness": 1, "quality": .5}}}
	v, ok := ratingVector(c, ratingSimilarity{Profile: 1 + 1e-9, HasProfile: true}, nil, false)
	if !ok || feature(Candidate{RatingFeatures: v}, "prof_cos0") != 1 || math.Abs(feature(Candidate{RatingFeatures: v}, "prof_cos_h45")-.55) > 1e-6 {
		t.Fatalf("rounded similarity at 1: %v %v", ok, v)
	}
	if _, ok := ratingVector(c, ratingSimilarity{Profile: 1.1, HasProfile: true}, nil, false); ok {
		t.Fatal("accepted an impossible similarity")
	}
	if _, ok := ratingVector(c, ratingSimilarity{MaxNeed: math.NaN(), Needs: 1}, nil, false); ok {
		t.Fatal("accepted a NaN similarity")
	}
}

func TestRatingReplacesSemanticOrderingWhenBothRun(t *testing.T) {
	e, s, _, rating, learned, _ := ratingFixture()
	e.ProfileSemanticBPS = 10000
	rating.by = "is_pgc"
	s.docs = []Document{profileDoc(1, 5, 1), profileDoc(2, 5, 1)}
	s.docs[1].AuthorID = 77
	s.cosine[1], s.cosine[2] = .6, .35
	x := runProfile(t, e)
	if got := fmt.Sprint(ids(x)); got != "[2 1]" || rating.calls != 1 || learned.calls != 0 {
		t.Fatalf("got %s rating=%d learned=%d", got, rating.calls, learned.calls)
	}
	for _, c := range x.Candidates {
		if c.SemanticRank == nil || !ratingRanked(c) || c.FinalScore == *c.SemanticRank {
			t.Fatalf("semantic candidates must be scored by the rating model: %+v", c)
		}
	}
	if s.vectorCalls != 1 {
		t.Fatalf("the semantic profile vector must be reused: %d reads", s.vectorCalls)
	}
	e.RatingLRBPS = 0
	if x = runProfile(t, e); fmt.Sprint(ids(x)) != "[1 2]" || ratingRanked(x.Candidates[0]) {
		t.Fatalf("control keeps semantic ordering: %v", ids(x))
	}
}

type statsFake struct {
	*profileFake
	counts map[int64]RatingCounts
	err    error
	reads  [][]int64
}

func (s *statsFake) ItemRatingStats(_ context.Context, ids []int64) (map[int64]RatingCounts, error) {
	s.reads = append(s.reads, append([]int64(nil), ids...))
	if s.err != nil {
		return nil, s.err
	}
	out := map[int64]RatingCounts{}
	for _, id := range ids {
		if c, ok := s.counts[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func challengerFixture() (*Engine, *statsFake, *ratingFake, *ratingFake) {
	e, s, _, rating, _, _ := ratingFixture()
	f := &statsFake{profileFake: s, counts: map[int64]RatingCounts{}}
	e.Sources = f
	next := &ratingFake{contract: discoverylr.RatingV2Contract, by: "it_pos_rate"}
	e.RatingNext, e.RatingNextBPS = next, 10000
	s.docs = []Document{profileDoc(1, 5, 1), profileDoc(2, 5, 1), profileDoc(3, 5, 1)}
	for id, cos := range map[int64]float64{1: .6, 2: .4, 3: .2} {
		s.cosine[id] = cos
	}
	return e, f, rating, next
}

func TestRatingV2VectorMatchesPythonFixture(t *testing.T) {
	raw, err := os.ReadFile("../discoverylr/testdata/discovery_rating_lr_v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Model json.RawMessage `json:"model"`
		Cases []struct {
			Candidate   Candidate        `json:"candidate"`
			Similarity  ratingSimilarity `json:"similarity"`
			PGC         bool             `json:"pgc"`
			Counts      RatingCounts     `json:"counts"`
			Features    []float64        `json:"features"`
			Probability float64          `json:"probability"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "model.json")
	if err = os.WriteFile(path, data.Model, 0600); err != nil {
		t.Fatal(err)
	}
	model := discoverylr.NewRatingV2(true, path, time.Hour)
	defer model.Close()
	if len(data.Cases) < 40 {
		t.Fatalf("fixture too small: %d", len(data.Cases))
	}
	for i, tc := range data.Cases {
		base, ok := ratingVector(tc.Candidate, tc.Similarity, tc.Candidate.Document.Channels, tc.PGC)
		heat, hok := ratingHeat(tc.Counts)
		got := append(base, heat...)
		if !ok || !hok || len(got) != len(discoverylr.RatingV2FeatureNames) || len(tc.Features) != len(got) {
			t.Fatalf("case %d: ok=%v/%v len=%d", i, ok, hok, len(got))
		}
		for j := range got {
			if math.Abs(got[j]-tc.Features[j]) > 1e-12 {
				t.Fatalf("case %d %s: got %v want %v", i, discoverylr.RatingV2FeatureNames[j], got[j], tc.Features[j])
			}
		}
		scores, ok := model.ScoreBatch([][]float64{got})
		if !ok || math.Abs(scores[0].Probability-tc.Probability) > 1e-9 || scores[0].Contract != discoverylr.RatingV2Contract {
			t.Fatalf("case %d: score %+v ok=%v want %v", i, scores, ok, tc.Probability)
		}
	}
}

func TestRatingChallengerAddsItemCounts(t *testing.T) {
	e, s, rating, next := challengerFixture()
	s.counts[3] = RatingCounts{One: 8, Two: 2}
	s.counts[1] = RatingCounts{Negative: 9, Zero: 1}
	x := runProfile(t, e)
	// Ordered by it_pos_rate: 3 has 10 positives of 10, 2 has none, 1 has nine -1s.
	if got := fmt.Sprint(ids(x)); got != "[3 2 1]" || next.calls != 1 || rating.calls != 0 {
		t.Fatalf("got %s next=%d incumbent=%d", got, next.calls, rating.calls)
	}
	for _, c := range x.Candidates {
		if c.LR == nil || c.LR.Contract != discoverylr.RatingV2Contract || len(c.RatingFeatures) != len(discoverylr.RatingV2FeatureNames) || !ratingRanked(c) {
			t.Fatalf("challenger attribution: %+v", c)
		}
	}
	top := x.Candidates[0].RatingFeatures
	want := []float64{math.Log1p(10) / math.Log(101), 11.0 / 13, 1.0 / 13}
	for i, v := range want {
		if math.Abs(top[len(discoverylr.RatingFeatureNames)+i]-v) > 1e-12 {
			t.Fatalf("heat input %d = %v, want %v", i, top[len(discoverylr.RatingFeatureNames)+i], v)
		}
	}
	if len(s.reads) != 1 || len(s.reads[0]) != 3 {
		t.Fatalf("one count read for all candidates: %v", s.reads)
	}
	if item := PublicItem(x.Candidates[0]); item.Match["score_kind"] != "positive_rating_probability" {
		t.Fatalf("public attribution: %+v", item.Match)
	}
}

func TestRatingChallengerFallsBackToIncumbent(t *testing.T) {
	e, s, rating, next := challengerFixture()
	s.err = fmt.Errorf("db down")
	x := runProfile(t, e)
	if next.calls != 0 || rating.calls != 1 {
		t.Fatalf("next=%d incumbent=%d", next.calls, rating.calls)
	}
	for _, c := range x.Candidates {
		if c.LR == nil || c.LR.Contract != discoverylr.RatingContract || len(c.RatingFeatures) != len(discoverylr.RatingFeatureNames) {
			t.Fatalf("incumbent must score the whole request: %+v", c)
		}
	}
}

func TestRatingChallengerWithoutModelKeepsIncumbent(t *testing.T) {
	e, _, rating, next := challengerFixture()
	next.fail = true
	x := runProfile(t, e)
	if rating.calls != 1 || len(x.Candidates) == 0 {
		t.Fatalf("incumbent=%d candidates=%d", rating.calls, len(x.Candidates))
	}
	for _, c := range x.Candidates {
		if c.LR == nil || c.LR.Contract != discoverylr.RatingContract || len(c.RatingFeatures) != len(discoverylr.RatingFeatureNames) {
			t.Fatalf("incumbent inputs must not carry challenger inputs: %+v", c)
		}
	}
}

func TestRatingChallengerScope(t *testing.T) {
	e, s, rating, next := challengerFixture()
	e.RatingNextBPS = 0
	runProfile(t, e)
	if next.calls != 0 || rating.calls != 1 || len(s.reads) != 0 {
		t.Fatalf("challenger off: next=%d incumbent=%d reads=%d", next.calls, rating.calls, len(s.reads))
	}
	e.RatingNextBPS, e.RatingLRBPS = 10000, 0
	runProfile(t, e)
	if next.calls != 0 || len(s.reads) != 0 {
		t.Fatal("the challenger only serves rating-ranked Agents")
	}
	e.RatingLRBPS = 10000
	e.Sources = s.profileFake
	runProfile(t, e)
	if next.calls != 0 || rating.calls != 2 {
		t.Fatal("a source without item counts keeps the incumbent")
	}
}

func TestRatingNextBucketIsIndependent(t *testing.T) {
	agree, n := 0, 2000
	for owner := int64(1); owner <= int64(n); owner++ {
		if bucketEnabled(ratingNextSalt, owner, 5000) == ratingEnabled(owner, 5000) {
			agree++
		}
	}
	if agree < n*4/10 || agree > n*6/10 {
		t.Fatalf("buckets correlated: %d/%d agree", agree, n)
	}
}
