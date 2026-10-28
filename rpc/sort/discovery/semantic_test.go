package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"eigenflux_server/pkg/need"
)

type profileFake struct {
	*sourceFake
	vector       []float32
	vectorErr    error
	cosine       map[int64]float64
	needCosine   map[int64]float64
	vecCosine    map[float32]map[int64]float64
	vecErr       map[float32]error
	cosineErr    error
	profileDocs  []Document
	vectorCalls  int
	cosineCalls  int
	recallCalls  int
	cosineIDs    [][]int64
	recallVector []float32
	recallSince  []int64
}

func (s *profileFake) ProfileVector(context.Context, int64) ([]float32, error) {
	s.vectorCalls++
	return s.vector, s.vectorErr
}
func (s *profileFake) Cosine(_ context.Context, v []float32, ids []int64) (map[int64]float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cosineCalls++
	s.cosineIDs = append(s.cosineIDs, append([]int64(nil), ids...))
	table := s.cosine
	if len(v) > 0 && v[0] == testNeedVector[0] {
		table = s.needCosine
	}
	if t, ok := s.vecCosine[v[0]]; ok {
		table = t
	}
	if err := s.vecErr[v[0]]; err != nil {
		return nil, err
	}
	out := map[int64]float64{}
	for _, id := range ids {
		if v, ok := table[id]; ok {
			out[id] = v
		}
	}
	return out, s.cosineErr
}
func (s *profileFake) RecallProfile(_ context.Context, _ Context, v []float32, since int64, _ int) ([]Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recallCalls++
	s.recallVector = v
	s.recallSince = append(s.recallSince, since)
	out := []Document{}
	for _, d := range s.profileDocs {
		if d.FreshAt >= since {
			out = append(out, d)
		}
	}
	for i := range out {
		score := 0.9
		out[i].DenseScore = &score
	}
	return out, nil
}

const testNow = int64(200 * 24 * 60 * 60 * 1000)

var testNeedVector = []float32{9, 1}

func profileDoc(id int64, lexical float64, ageDays int64) Document {
	return Document{Ref: SourceRef{Type: Broadcast, ID: id}, GroupID: id, Active: true, Visible: true, AuthorID: 99, Version: "v1", Lexical: lexical, FreshAt: testNow - ageDays*day}
}

func profileFixture() (*Engine, *profileFake) {
	e, f, _ := profileFixtureWithStore()
	return e, f
}

func profileFixtureWithStore() (*Engine, *profileFake, *memStore) {
	e, s, store := engineFixture()
	s.owner = OwnerContext{Clauses: []string{"battery", "solid state"}}
	f := &profileFake{sourceFake: s, vector: []float32{1, 0}, cosine: map[int64]float64{}, needCosine: map[int64]float64{}}
	e.Sources = f
	e.ProfileSemanticBPS = 10000
	return e, f, store
}

func runProfile(t *testing.T, e *Engine) Execution {
	t.Helper()
	x, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}, Limit: 20}, Recommendation, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func ids(x Execution) []int64 {
	out := []int64{}
	for _, c := range x.Candidates {
		out = append(out, c.Document.Ref.ID)
	}
	return out
}

func TestProfileMinCosineTightensWithAge(t *testing.T) {
	for _, tc := range []struct {
		age  int64
		want float64
	}{{0, .30}, {30, .30}, {31, .40}, {90, .40}, {91, .50}} {
		if got := profileMinCosine(testNow-tc.age*day, testNow); got != tc.want {
			t.Fatalf("age %d: got %v want %v", tc.age, got, tc.want)
		}
	}
	if got := profileMinCosine(0, testNow); got != .50 {
		t.Fatalf("unknown age must use the strictest floor: %v", got)
	}
}

func TestProfileSemanticBucketIsIndependentOfRefill(t *testing.T) {
	same, n := 0, 2000
	for owner := int64(1); owner <= int64(n); owner++ {
		if profileSemanticEnabled(owner, 5000) == consumedRefillEnabled(owner, 5000) {
			same++
		}
	}
	if same < n*4/10 || same > n*6/10 {
		t.Fatalf("buckets correlated: %d/%d agree", same, n)
	}
	if profileSemanticEnabled(1, 0) || !profileSemanticEnabled(1, 10000) {
		t.Fatal("bounds")
	}
}

func TestProfileSemanticOffKeepsRuleScoring(t *testing.T) {
	e, s := profileFixture()
	e.ProfileSemanticBPS = 0
	s.docs = []Document{profileDoc(1, 5, 1)}
	s.cosine[1] = .1
	x := runProfile(t, e)
	if s.vectorCalls != 0 || s.cosineCalls != 0 || s.recallCalls != 0 {
		t.Fatalf("off path read profile evidence: %+v", s)
	}
	if len(x.Candidates) != 1 || x.Candidates[0].Score.Kind != "rule_score" {
		t.Fatalf("off path changed: %+v", x.Candidates)
	}
}

func TestProfileSemanticAdmitsAndOrders(t *testing.T) {
	e, s := profileFixture()
	s.docs = []Document{
		profileDoc(1, 5, 1),   // strong keyword, unrelated profile
		profileDoc(2, 5, 1),   // keyword and profile match
		profileDoc(3, 5, 1),   // no item embedding
		profileDoc(4, 5, 60),  // 60 days old, below the 0.40 floor
		profileDoc(5, 5, 120), // 120 days old, above the 0.50 floor
	}
	s.profileDocs = []Document{
		profileDoc(6, 0, 2), // no keyword match at all, close to the profile
		profileDoc(8, 0, 2), // no keyword match, unrelated: neither rule nor cosine admits it
	}
	s.channels = map[string][]Document{"friend": {profileDoc(7, 0, 1)}}
	e.FriendFeedEnabled = true
	for id, cos := range map[int64]float64{1: .20, 2: .45, 4: .35, 5: .55, 6: .60, 7: .05, 8: .10} {
		s.cosine[id] = cos
	}
	x := runProfile(t, e)
	got := fmt.Sprint(ids(x))
	// Rule-eligible candidates stay (positives first); similarity only adds
	// candidates and orders them. Missing embeddings sort last in their context.
	if got != "[6 5 2 4 1 7 3]" {
		t.Fatalf("got %s: %+v", got, x.PartialReasons)
	}
	if s.recallCalls != 2 || len(s.recallVector) != 2 {
		t.Fatalf("each profile lane must run once per request: %d", s.recallCalls)
	}
	fresh := false
	for _, since := range s.recallSince {
		fresh = fresh || since == testNow-profileFreshDays*day
	}
	if !fresh {
		t.Fatalf("missing recent-only lane: %v", s.recallSince)
	}
	if s.cosineCalls != 1 {
		t.Fatalf("similarity must be read once for all contexts: %d %v", s.cosineCalls, s.cosineIDs)
	}
	for _, c := range x.Candidates {
		if c.SemanticRank == nil || c.FinalScore != *c.SemanticRank {
			t.Fatalf("missing rank: %+v", c)
		}
		if c.Document.Ref.ID == 3 {
			if c.SemanticTier != semanticTierMissing || *c.SemanticRank < 0 || *c.SemanticRank > 1 || c.Score.Features["profile_embedding_missing"] != 1 {
				t.Fatalf("missing embedding must sort last with a positive rank: %+v", c)
			}
			continue
		}
		if c.Score.Kind != "rule_score" || !strings.HasSuffix(c.Score.Version, profileSemanticVersion) || c.Score.Features["profile_cosine"] != s.cosine[c.Document.Ref.ID] {
			t.Fatalf("missing attribution: %+v", c)
		}
		rule := ScoreRules(c.Context, c.Document, e.Rules[Broadcast][Recommendation], testNow)
		if c.Score.Value != rule.Value || c.Score.Relevance != rule.Relevance || c.Score.Value < 0 || c.Score.Value > 1 {
			t.Fatalf("rule score contract changed: %+v vs %+v", c.Score, rule)
		}
		if c.Document.Ref.ID == 6 && c.Document.DenseScore != nil {
			t.Fatal("profile kNN score must not become Need semantic evidence")
		}
	}
	b, _ := json.Marshal(x.Candidates[0].Document)
	if strings.Contains(string(b), "profile") {
		t.Fatalf("profile evidence leaked into the document snapshot: %s", b)
	}
}

func TestProfileSemanticSkipsLearnedRanker(t *testing.T) {
	e, s := profileFixture()
	lr := &learnedFake{}
	e.Learned = lr
	s.docs = []Document{profileDoc(1, 5, 1)}
	s.cosine[1] = .5
	if x := runProfile(t, e); len(x.Candidates) != 1 || x.Candidates[0].LR != nil || lr.calls != 0 {
		t.Fatalf("%+v", x.Candidates)
	}
	e.ProfileSemanticBPS = 0
	if runProfile(t, e); lr.calls == 0 {
		t.Fatal("control must keep the learned ranker")
	}
}

func TestProfileSemanticDegradesToRules(t *testing.T) {
	for name, mutate := range map[string]func(*profileFake){
		"no vector":    func(s *profileFake) { s.vector = nil },
		"vector error": func(s *profileFake) { s.vectorErr = fmt.Errorf("db down") },
		"cosine error": func(s *profileFake) { s.cosineErr = fmt.Errorf("es down") },
		"zero vector":  func(s *profileFake) { s.vector = []float32{} },
	} {
		t.Run(name, func(t *testing.T) {
			e, s := profileFixture()
			s.docs = []Document{profileDoc(1, 5, 1)}
			s.cosine[1] = .1
			mutate(s)
			x := runProfile(t, e)
			if len(x.Candidates) != 1 || x.Candidates[0].SemanticRank != nil || strings.Contains(x.Candidates[0].Score.Version, "profile") {
				t.Fatalf("expected rule fallback: %+v", x.Candidates)
			}
			if s.cosineCalls > 1 {
				t.Fatalf("failed similarity read retried per context: %d", s.cosineCalls)
			}
			found := false
			for _, r := range x.PartialReasons {
				found = found || strings.HasPrefix(r, "profile_semantic:")
			}
			if !found {
				t.Fatalf("fallback not reported: %v", x.PartialReasons)
			}
		})
	}
}

func TestProfileSemanticUnsupportedSources(t *testing.T) {
	e, s, _ := engineFixture()
	s.owner = OwnerContext{Clauses: []string{"battery"}}
	s.docs = []Document{profileDoc(1, 5, 1)}
	e.ProfileSemanticBPS = 10000
	x := runProfile(t, e)
	if len(x.Candidates) != 1 || fmt.Sprint(x.PartialReasons) != "[profile_semantic:unsupported]" {
		t.Fatalf("%+v", x)
	}
}

func TestDenseQueryExcludesConsumedGroups(t *testing.T) {
	c := Context{Vector: []float32{1, 0}, Filters: Filters{ExcludeAuthors: []string{"2"}}}
	before, err := Query(c, Broadcast, "dense", 80)
	if err != nil {
		t.Fatal(err)
	}
	c.excludeConsumedGroups = []int64{11}
	after, err := Query(c, Broadcast, "dense", 80)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if strings.Contains(string(a), `"group_id"`) || !strings.Contains(string(b), `"group_id":[11]`) {
		t.Fatalf("dense exclusion: %s / %s", a, b)
	}
}

func TestProfileSemanticKeepsCapturedNeedWithoutVector(t *testing.T) {
	e, s, store := profileFixtureWithStore()
	store.active = []need.Snapshot{capturedFixture(9, Broadcast)}
	lr := &learnedFake{}
	e.Learned = lr
	s.docs = []Document{profileDoc(1, 5, 1)}
	s.cosine[1] = .05 // A Need far from the standing profile.
	x := runProfile(t, e)
	if len(x.Candidates) != 1 || x.Candidates[0].Context.CapturedNeed == nil || x.Candidates[0].SemanticRank != nil {
		t.Fatalf("captured Need must keep rule relevance: %+v", x.Candidates)
	}
	if lr.calls == 0 || x.Candidates[0].LR == nil {
		t.Fatal("Need candidates keep the learned ranker")
	}
	if s.recallCalls != 0 || s.cosineCalls != 0 {
		t.Fatalf("no Profile context, no profile reads: recall=%d cosine=%d", s.recallCalls, s.cosineCalls)
	}
}

func TestNeedSemanticAdmitsAndOrdersByNeedVector(t *testing.T) {
	e, s, store := profileFixtureWithStore()
	store.active = []need.Snapshot{capturedFixture(9, Broadcast)}
	e.Compiler.NeedVectors = cacheLookupFunc(func(context.Context, int64, string, string) ([]float32, error) {
		return testNeedVector, nil
	})
	lr := &learnedFake{}
	e.Learned = lr
	s.docs = []Document{
		profileDoc(11, 0, 1), // vector-only recall, close to the Need
		profileDoc(12, 5, 1), // keyword match, far from the Need: rule keeps it
		profileDoc(13, 0, 1), // vector-only recall, far from the Need
		profileDoc(14, 5, 1), // keyword match and close to the Need
	}
	for id, cos := range map[int64]float64{11: .50, 12: .10, 13: .10, 14: .40} {
		s.needCosine[id] = cos
		s.cosine[id] = .05 // the standing profile disagrees; the Need vector decides
	}
	x := runProfile(t, e)
	if got := fmt.Sprint(ids(x)); got != "[11 14 12]" {
		t.Fatalf("got %s: %+v", got, x.PartialReasons)
	}
	for _, c := range x.Candidates {
		if c.Context.CapturedNeed == nil || c.SemanticRank == nil || c.LR != nil || !strings.HasSuffix(c.Score.Version, needSemanticVersion) || c.Score.Features["need_cosine"] != s.needCosine[c.Document.Ref.ID] {
			t.Fatalf("missing Need attribution: %+v", c)
		}
	}
	if s.recallCalls != 0 || s.cosineCalls != 1 || lr.calls != 0 {
		t.Fatalf("one Need similarity read, no profile recall or LR: recall=%d cosine=%d lr=%d", s.recallCalls, s.cosineCalls, lr.calls)
	}
	e.ProfileSemanticBPS = 0
	if x = runProfile(t, e); fmt.Sprint(ids(x)) != "[12 14]" || lr.calls == 0 {
		t.Fatalf("control must keep rule eligibility and LR: %v", ids(x))
	}
}

func TestNeedSemanticDoesNotNeedProfileVector(t *testing.T) {
	e, s, store := profileFixtureWithStore()
	store.active = []need.Snapshot{capturedFixture(9, Broadcast)}
	e.Compiler.NeedVectors = cacheLookupFunc(func(context.Context, int64, string, string) ([]float32, error) {
		return testNeedVector, nil
	})
	s.vector = nil
	s.docs = []Document{profileDoc(11, 0, 1)}
	s.needCosine[11] = .5
	x := runProfile(t, e)
	if fmt.Sprint(ids(x)) != "[11]" || x.Candidates[0].SemanticRank == nil || !strings.HasSuffix(x.Candidates[0].Score.Version, needSemanticVersion) {
		t.Fatalf("Need similarity must not depend on a profile vector: %v %+v", x.PartialReasons, x.Candidates)
	}
}

func TestNeedSemanticCosineFailureKeepsRules(t *testing.T) {
	e, s, store := profileFixtureWithStore()
	store.active = []need.Snapshot{capturedFixture(9, Broadcast)}
	e.Compiler.NeedVectors = cacheLookupFunc(func(context.Context, int64, string, string) ([]float32, error) {
		return testNeedVector, nil
	})
	s.cosineErr = fmt.Errorf("es down")
	s.docs = []Document{profileDoc(11, 0, 1), profileDoc(12, 5, 1)}
	x := runProfile(t, e)
	if fmt.Sprint(ids(x)) != "[12]" || x.Candidates[0].SemanticRank != nil || !strings.Contains(fmt.Sprint(x.PartialReasons), "need_semantic:cosine_unavailable") || s.cosineCalls != 1 {
		t.Fatalf("expected rule fallback: %v %+v", x.PartialReasons, x.Candidates)
	}
}

func TestProfileSemanticControlRecordsShadowCosine(t *testing.T) {
	e, s := profileFixture()
	bps := 10000
	for bps > 0 && profileSemanticEnabled(1, bps) {
		bps--
	}
	if bps == 0 {
		t.Skip("owner 1 hashes to bucket 0")
	}
	e.ProfileSemanticBPS = bps
	lr := &learnedFake{}
	e.Learned = lr
	s.docs = []Document{profileDoc(1, 5, 1)}
	s.cosine[1] = .1 // Below every treatment floor; control must still deliver it.
	x := runProfile(t, e)
	if len(x.Candidates) != 1 || x.Candidates[0].SemanticRank != nil || strings.Contains(x.Candidates[0].Score.Version, "profile") {
		t.Fatalf("control behaviour changed: %+v", x.Candidates)
	}
	if x.Candidates[0].Score.Features["profile_cosine"] != .1 || lr.calls == 0 {
		t.Fatalf("shadow feature or LR missing: %+v", x.Candidates[0])
	}
	if s.recallCalls != 0 || len(x.PartialReasons) != 0 {
		t.Fatalf("control must not run profile recall or report reasons: %d %v", s.recallCalls, x.PartialReasons)
	}
	s.vector = nil
	if x = runProfile(t, e); len(x.Candidates) != 1 || len(x.PartialReasons) != 0 {
		t.Fatalf("control without a vector must stay silent: %+v", x)
	}
}

func TestDenseQueryRecentOnly(t *testing.T) {
	c := Context{Vector: []float32{1, 0}, createdSince: 1000}
	q, err := Query(c, Broadcast, "dense", 40)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(q)
	if !strings.Contains(string(b), `"created_at":{"gte":"1970-01-01T00:00:01Z"}`) {
		t.Fatalf("recent-only filter missing: %s", b)
	}
	c.createdSince = 0
	q, _ = Query(c, Broadcast, "dense", 40)
	b, _ = json.Marshal(q)
	if strings.Contains(string(b), "created_at") {
		t.Fatalf("unbounded lane must not filter by date: %s", b)
	}
}

func needVectorsByInput(vectors map[int64][]float32) NeedVectorLookup {
	return cacheLookupFunc(func(_ context.Context, id int64, _, _ string) ([]float32, error) {
		return vectors[id], nil
	})
}

func TestNeedSemanticRanksAcrossNeeds(t *testing.T) {
	e, s, store := profileFixtureWithStore()
	store.active = []need.Snapshot{capturedFixture(9, Broadcast), capturedFixture(10, Broadcast)}
	e.Compiler.NeedVectors = needVectorsByInput(map[int64][]float32{9: {7, 1}, 10: {8, 1}})
	s.docs = []Document{profileDoc(21, 0, 1), profileDoc(22, 0, 1), profileDoc(23, 5, 1)}
	s.vecCosine = map[float32]map[int64]float64{
		7: {21: .9, 22: .1, 23: .1},
		8: {21: .1, 22: .8, 23: .1},
	}
	x := runProfile(t, e)
	// Without cross-context ranking the first Need's rule-only item would
	// precede the second Need's strong match: [21 23 22].
	if got := fmt.Sprint(ids(x)); got != "[21 22 23]" {
		t.Fatalf("got %s", got)
	}
	if s.cosineCalls != 2 {
		t.Fatalf("one similarity read per Need vector: %d", s.cosineCalls)
	}
}

func TestNeedSemanticUnembeddedItemKeepsRuleEligibility(t *testing.T) {
	e, s, store := profileFixtureWithStore()
	store.active = []need.Snapshot{capturedFixture(9, Broadcast)}
	e.Compiler.NeedVectors = needVectorsByInput(map[int64][]float32{9: testNeedVector})
	s.docs = []Document{profileDoc(31, 5, 1), profileDoc(32, 5, 1)}
	s.needCosine[32] = .2 // 31 has no item embedding
	x := runProfile(t, e)
	if got := fmt.Sprint(ids(x)); got != "[32 31]" {
		t.Fatalf("got %s", got)
	}
	c := x.Candidates[1]
	if c.SemanticTier != semanticTierMissing || c.Score.Features["need_embedding_missing"] != 1 || !strings.HasSuffix(c.Score.Version, needSemanticVersion) {
		t.Fatalf("unembedded Need item attribution: %+v", c)
	}
}

func TestNeedSemanticFailureLeavesProfileScoring(t *testing.T) {
	e, s, store := profileFixtureWithStore()
	store.active = []need.Snapshot{capturedFixture(9, Broadcast)}
	e.Compiler.NeedVectors = needVectorsByInput(map[int64][]float32{9: testNeedVector})
	e.FriendFeedEnabled = true
	s.vecErr = map[float32]error{testNeedVector[0]: fmt.Errorf("es down")}
	s.docs = []Document{profileDoc(41, 5, 1)}
	s.channels = map[string][]Document{"friend": {profileDoc(42, 0, 1)}}
	s.cosine[42] = .6
	x := runProfile(t, e)
	byID := map[int64]Candidate{}
	for _, c := range x.Candidates {
		byID[c.Document.Ref.ID] = c
	}
	if byID[41].SemanticRank != nil || byID[42].SemanticRank == nil || byID[42].Score.Features["profile_cosine"] != .6 {
		t.Fatalf("Need read failure must not disable profile scoring: %+v", x.Candidates)
	}
	if !strings.Contains(fmt.Sprint(x.PartialReasons), "need_semantic:cosine_unavailable") {
		t.Fatalf("missing reason: %v", x.PartialReasons)
	}
}

func TestNeedSemanticControlReadsNoNeedVector(t *testing.T) {
	e, s, store := profileFixtureWithStore()
	bps := 10000
	for bps > 0 && profileSemanticEnabled(1, bps) {
		bps--
	}
	if bps == 0 {
		t.Skip("owner 1 hashes to bucket 0")
	}
	e.ProfileSemanticBPS = bps
	store.active = []need.Snapshot{capturedFixture(9, Broadcast)}
	e.Compiler.NeedVectors = needVectorsByInput(map[int64][]float32{9: testNeedVector})
	s.docs = []Document{profileDoc(51, 5, 1)}
	s.needCosine[51] = .9
	x := runProfile(t, e)
	if len(x.Candidates) != 1 || x.Candidates[0].SemanticRank != nil || s.cosineCalls != 0 || len(x.PartialReasons) != 0 {
		t.Fatalf("control Need contexts must stay unchanged: calls=%d %+v %v", s.cosineCalls, x.Candidates, x.PartialReasons)
	}
}

type refillSemanticFake struct {
	*refillFake
	profile *profileFake
}

func (s refillSemanticFake) ProfileVector(ctx context.Context, owner int64) ([]float32, error) {
	return s.profile.ProfileVector(ctx, owner)
}
func (s refillSemanticFake) Cosine(ctx context.Context, v []float32, ids []int64) (map[int64]float64, error) {
	return s.profile.Cosine(ctx, v, ids)
}
func (s refillSemanticFake) RecallProfile(ctx context.Context, c Context, v []float32, since int64, limit int) ([]Document, error) {
	return s.profile.RecallProfile(ctx, c, v, since, limit)
}

func TestSemanticScoresRefillCandidates(t *testing.T) {
	e, rf := refillFixture()
	pf := &profileFake{sourceFake: rf.sourceFake, vector: []float32{1, 0}, cosine: map[int64]float64{}, needCosine: map[int64]float64{}}
	e.Sources = refillSemanticFake{rf, pf}
	e.ProfileSemanticBPS = 10000
	for i := int64(1); i <= 3; i++ {
		rf.docs = append(rf.docs, refillDoc(i))
		rf.seen[fmt.Sprintf("broadcast:%d", i)] = true
		rf.groups = append(rf.groups, i)
	}
	for i := int64(61); i <= 63; i++ {
		rf.pool = append(rf.pool, refillDoc(i))
	}
	pf.cosine[61], pf.cosine[62], pf.cosine[63] = .3, .9, .1
	x, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}, Limit: 20}, Recommendation, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(ids(x)); got != "[62 61 63]" || x.Refill == nil || rf.refillCalls == 0 {
		t.Fatalf("refill candidates must be similarity-ranked: %s %+v", got, x.Refill)
	}
	for _, c := range x.Candidates {
		if c.SemanticRank == nil {
			t.Fatalf("unscored refill candidate: %+v", c)
		}
	}
}
