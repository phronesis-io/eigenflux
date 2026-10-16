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
func (s *profileFake) ProfileCosine(_ context.Context, _ []float32, ids []int64) (map[int64]float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cosineCalls++
	s.cosineIDs = append(s.cosineIDs, append([]int64(nil), ids...))
	out := map[int64]float64{}
	for _, id := range ids {
		if v, ok := s.cosine[id]; ok {
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
	f := &profileFake{sourceFake: s, vector: []float32{1, 0}, cosine: map[int64]float64{}}
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

func TestProfileSemanticGatesRecallsAndOrders(t *testing.T) {
	e, s := profileFixture()
	s.docs = []Document{
		profileDoc(1, 5, 1),   // strong keyword, unrelated profile
		profileDoc(2, 5, 1),   // keyword and profile match
		profileDoc(3, 5, 1),   // no item embedding
		profileDoc(4, 5, 60),  // 60 days old, below the 0.40 floor
		profileDoc(5, 5, 120), // 120 days old, above the 0.50 floor
	}
	s.profileDocs = []Document{profileDoc(6, 0, 2)} // no keyword match at all
	s.channels = map[string][]Document{"friend": {profileDoc(7, 0, 1)}}
	e.FriendFeedEnabled = true
	for id, cos := range map[int64]float64{1: .20, 2: .45, 4: .35, 5: .55, 6: .60, 7: .05} {
		s.cosine[id] = cos
	}
	x := runProfile(t, e)
	got := fmt.Sprint(ids(x))
	if got != "[6 5 2 7]" {
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
		if c.Score.Kind != "rule_score" || !strings.HasSuffix(c.Score.Version, profileSemanticVersion) || c.Score.Features["profile_cosine"] != s.cosine[c.Document.Ref.ID] || c.ProfileRank == nil || c.FinalScore != *c.ProfileRank {
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
			if len(x.Candidates) != 1 || x.Candidates[0].ProfileRank != nil || strings.Contains(x.Candidates[0].Score.Version, "profile") {
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

func TestProfileSemanticKeepsCapturedNeedRelevance(t *testing.T) {
	e, s, store := profileFixtureWithStore()
	store.active = []need.Snapshot{capturedFixture(9, Broadcast)}
	lr := &learnedFake{}
	e.Learned = lr
	s.docs = []Document{profileDoc(1, 5, 1)}
	s.cosine[1] = .05 // A Need far from the standing profile.
	x := runProfile(t, e)
	if len(x.Candidates) != 1 || x.Candidates[0].Context.CapturedNeed == nil || x.Candidates[0].ProfileRank != nil {
		t.Fatalf("captured Need must keep rule relevance: %+v", x.Candidates)
	}
	if lr.calls == 0 || x.Candidates[0].LR == nil {
		t.Fatal("Need candidates keep the learned ranker")
	}
	if s.recallCalls != 0 || s.cosineCalls != 0 {
		t.Fatalf("no Profile context, no profile reads: recall=%d cosine=%d", s.recallCalls, s.cosineCalls)
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
	if len(x.Candidates) != 1 || x.Candidates[0].ProfileRank != nil || strings.Contains(x.Candidates[0].Score.Version, "profile") {
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
