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
	calls int
	fail  bool
	by    string
}

var cosBest = slices.Index(discoverylr.RatingFeatureNames, "cos_best0")

func (f *ratingFake) ScoreBatch(v [][]float64) ([]discoverylr.Result, bool) {
	f.calls++
	if f.fail {
		return nil, false
	}
	at := cosBest
	if f.by != "" {
		at = slices.Index(discoverylr.RatingFeatureNames, f.by)
	}
	out := make([]discoverylr.Result, len(v))
	for i, x := range v {
		out[i] = discoverylr.Result{ModelVersion: "rating-test", Contract: discoverylr.RatingContract, Probability: .1 + .8*x[at]}
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
	if got := fmt.Sprint(ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 10)})); got != "[9 4 1 2 5 3]" {
		t.Fatalf("got %s", got)
	}
	slices.Reverse(in)
	if got := fmt.Sprint(ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 10)})); got != "[9 4 1 2 5 3]" {
		t.Fatalf("order depends on input order: %s", got)
	}
	in[0].Context.Priority = 1
	in[0].Context.CapturedNeed = nil
	if got := fmt.Sprint(ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 10)})); got != "[9 4 1 2 5 3]" {
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
