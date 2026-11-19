package discovery

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/need"
	"eigenflux_server/rpc/sort/discoverylr"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func attributionCandidate(ctx int64, cosine, probability float64, channels ...string) Candidate {
	return Candidate{Context: Context{ID: ctx, CapturedNeed: &need.Snapshot{}, Vector: []float32{1}},
		Document: Document{Ref: SourceRef{Broadcast, 10}, Channels: channels},
		Score:    Score{Eligible: true, Features: map[string]float64{"need_cosine": cosine}},
		LR:       &discoverylr.Result{Contract: discoverylr.RatingContract, Probability: probability}, FinalScore: probability}
}

func TestCosineReferenceAttributionBeforeRatingOrder(t *testing.T) {
	low := attributionCandidate(1, .3, .9, "swing_i2i")
	mid := attributionCandidate(2, .5, .8, "swing_i2i")
	high := attributionCandidate(3, .7, .7, "swing_i2i")
	for _, reverse := range []bool{false, true} {
		in := []Candidate{low, mid, high}
		if reverse {
			slices.Reverse(in)
		}
		control := Merge(in, []Kind{Broadcast}, Recommendation, 20, NeedAttributionOff, nil)
		require.Equal(t, int64(1), control[0].Context.ID)
		got := Merge(in, []Kind{Broadcast}, Recommendation, 20, NeedAttributionCosine, nil)
		require.Len(t, got, 1)
		require.Equal(t, int64(3), got[0].Context.ID)
		require.Equal(t, .7, got[0].FinalScore, "winning copy retains its LR score")
		require.Equal(t, Merge(in, []Kind{Broadcast}, Search, 20, NeedAttributionOff, nil), Merge(in, []Kind{Broadcast}, Search, 20, NeedAttributionCosine, nil))
	}
	mid.Document.Channels = []string{"dense"}
	got := Merge([]Candidate{low, mid, high}, []Kind{Broadcast}, Recommendation, 20, NeedAttributionCosine, nil)
	require.Equal(t, int64(2), got[0].Context.ID, "own recall outranks pooled cosine")
	require.ElementsMatch(t, []string{"dense", "swing_i2i"}, got[0].Document.Channels, "source caps retain union attribution")
	require.Equal(t, []string{"dense"}, mid.Document.Channels, "input provenance is not mutated")
	low.Reasons = []string{"inject:0"}
	got = Merge([]Candidate{mid, high, low}, []Kind{Broadcast}, Recommendation, 20, NeedAttributionCosine, nil)
	require.Equal(t, int64(1), got[0].Context.ID, "injection must survive ownership")
	require.True(t, injected(got[0]))
	low.Score.Eligible = false
	got = Merge([]Candidate{low, mid, high}, []Kind{Broadcast}, Recommendation, 20, NeedAttributionCosine, nil)
	require.Equal(t, int64(2), got[0].Context.ID, "ineligible injection cannot discard a valid copy")
}

func TestCosineReferenceMissingAndTiedCosine(t *testing.T) {
	a, b := attributionCandidate(1, .4, .8, "dense"), attributionCandidate(2, .4, .9, "dense")
	got := Merge([]Candidate{a, b}, []Kind{Broadcast}, Recommendation, 20, NeedAttributionCosine, nil)
	require.Equal(t, int64(2), got[0].Context.ID, "ties preserve existing LR order")
	a.Score.Features, b.Score.Features = nil, nil
	a.RatingFeatures = make([]float64, len(discoverylr.RatingFeatureNames))
	b.RatingFeatures = make([]float64, len(discoverylr.RatingFeatureNames))
	i := slices.Index(discoverylr.RatingFeatureNames, "need_cos_ctx0")
	a.RatingFeatures[i], b.RatingFeatures[i] = .8, .3
	got = Merge([]Candidate{a, b}, []Kind{Broadcast}, Recommendation, 20, NeedAttributionCosine, nil)
	require.Equal(t, int64(1), got[0].Context.ID, "rating evidence works without the semantic experiment")
	a.RatingFeatures, b.RatingFeatures = nil, nil
	got = Merge([]Candidate{a, b}, []Kind{Broadcast}, Recommendation, 20, NeedAttributionCosine, nil)
	require.Equal(t, int64(2), got[0].Context.ID, "missing evidence preserves existing order")
}

type needRecallSource struct {
	*refillFake
	mu      sync.Mutex
	queries map[string]map[string]any
	batches []int
	timeout bool
	cancel  context.CancelFunc
}

func (s *needRecallSource) ConsumedGroups(ctx context.Context, owner int64) ([]int64, bool, error) {
	if s.cancel != nil {
		s.cancel()
	}
	if s.timeout {
		s.historyCalls++
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
	return s.refillFake.ConsumedGroups(ctx, owner)
}
func (s *needRecallSource) Recall(ctx context.Context, c Context, k Kind, ch string, n int) ([]Document, error) {
	if ch == "lexical" || ch == "dense" {
		q, err := Query(c, k, ch, n)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.queries[fmt.Sprintf("%d:%s:%d", c.ID, ch, n)] = q
		s.mu.Unlock()
	}
	return s.sourceFake.Recall(ctx, c, k, ch, n)
}
func (s *needRecallSource) Hydrate(ctx context.Context, owner int64, m Mode, docs []Document) ([]Document, error) {
	s.batches = append(s.batches, len(docs))
	return s.sourceFake.Hydrate(ctx, owner, m, docs)
}
func needFixture() (*Engine, *needRecallSource) {
	e, f, _ := engineFixture()
	s := &needRecallSource{refillFake: &refillFake{sourceFake: f, groups: []int64{91, 92}}, queries: map[string]map[string]any{}}
	e.Sources = s
	for id := int64(1); id <= 3; id++ {
		e.frozenContexts = append(e.frozenContexts, Context{ID: id, OwnerID: 1, State: "active", Origin: "need_input", Query: "design", Vector: []float32{1, 0}, Kinds: []Kind{Broadcast}, CapturedNeed: &need.Snapshot{InputID: id}})
	}
	for id := int64(1); id <= 320; id++ {
		f.docs = append(f.docs, refillDoc(id))
	}
	return e, s
}
func TestNeedRelativeBucketStableAndBounded(t *testing.T) {
	on := 0
	for owner := int64(1); owner <= 1000; owner++ {
		got := bucketEnabled(needRelativeSalt, owner, 5000)
		require.Equal(t, got, bucketEnabled(needRelativeSalt, owner, 5000))
		if got {
			require.True(t, bucketEnabled(needRelativeSalt, owner, 7500))
			on++
		}
		require.False(t, bucketEnabled(needRelativeSalt, owner, 0))
		require.True(t, bucketEnabled(needRelativeSalt, owner, 10000))
	}
	require.InDelta(t, 500, on, 75)
}
func TestHydrateHistogramRecordsSuccessAndFailure(t *testing.T) {
	h := metrics.DiscoveryHydrateDuration.WithLabelValues(string(Recommendation))
	count := func() uint64 {
		m := &dto.Metric{}
		require.NoError(t, h.(interface{ Write(*dto.Metric) error }).Write(m))
		return m.Histogram.GetSampleCount()
	}
	before := count()
	s := &Source{}
	_, err := s.Hydrate(context.Background(), 1, Recommendation, nil)
	require.NoError(t, err)
	_, err = s.Hydrate(context.Background(), 1, Recommendation, make([]Document, 1001))
	require.Error(t, err)
	require.Equal(t, before+2, count())
}

// everyTurn reserves every position and admits every cosine: plain Need round robin.
var everyTurn = &NeedMix{Numerator: 1, Denominator: 1, MinNeedCosine: -1}

func roundRobinCandidate(ctx, id int64, probability float64, channels ...string) Candidate {
	c := attributionCandidate(ctx, .5, probability, channels...)
	c.Document.Ref.ID = id
	return c
}

func TestNeedRelativeRoundRobinPreservesLocalRanking(t *testing.T) {
	in := []Candidate{
		roundRobinCandidate(1, 101, .9, "dense"), roundRobinCandidate(1, 102, .8, "dense"), roundRobinCandidate(1, 103, .7, "dense"),
		roundRobinCandidate(2, 201, .2, "dense"), roundRobinCandidate(2, 202, .1, "dense"),
		roundRobinCandidate(3, 301, .6, "dense"), roundRobinCandidate(3, 302, .5, "dense"),
	}
	for _, reverse := range []bool{false, true} {
		if reverse {
			slices.Reverse(in)
		}
		got := Merge(in, []Kind{Broadcast}, Recommendation, 20, NeedAttributionRelative, everyTurn)
		require.Equal(t, []int64{101, 201, 301, 102, 202, 302, 103}, ids(Execution{Candidates: got}))
		require.Equal(t, []int64{101, 201, 301, 102}, ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 4, NeedAttributionRelative, everyTurn)}))
		for _, c := range got {
			require.Equal(t, c.LR.Probability, c.FinalScore, "mixing must not rewrite scores")
		}
		require.Equal(t, []int64{101, 102, 103, 301, 302, 201, 202}, ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 20, NeedAttributionOff, nil)}))
		require.Equal(t, []int64{101, 102, 103, 301, 302, 201, 202}, ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 20, NeedAttributionRelative, nil)}), "no need_mix keeps global order")
		require.Equal(t, Merge(in, []Kind{Broadcast}, Recommendation, 20, NeedAttributionOff, nil), Merge(in, []Kind{Broadcast}, Recommendation, 20, NeedAttributionOff, everyTurn), "mixing needs the Need relative attribution bucket")
		require.Equal(t, Merge(in, []Kind{Broadcast}, Search, 20, NeedAttributionOff, nil), Merge(in, []Kind{Broadcast}, Search, 20, NeedAttributionRelative, everyTurn))
	}
}

func TestNeedRelativeRoundRobinSkipsDuplicatesWithinTurn(t *testing.T) {
	in := []Candidate{
		roundRobinCandidate(1, 11, .9, "dense"), roundRobinCandidate(1, 12, .8, "dense"), roundRobinCandidate(1, 13, .7, "dense"),
		roundRobinCandidate(2, 21, .3, "dense"), roundRobinCandidate(2, 22, .2, "dense"), roundRobinCandidate(2, 23, .1, "dense"),
	}
	in[0].Document.GroupID, in[3].Document.GroupID = 50, 50
	in[4].Score.Eligible = false
	got := Merge(in, []Kind{Broadcast}, Recommendation, 20, NeedAttributionRelative, everyTurn)
	require.Equal(t, []int64{11, 23, 12, 13}, ids(Execution{Candidates: got}), "duplicate and ineligible candidates do not consume a Need's turn")
}

func TestNeedRelativeRoundRobinPreservesPriorityFallbackAndInjection(t *testing.T) {
	injectedCopy := roundRobinCandidate(1, 11, .01, "new_ugc_recall")
	injectedCopy.Reasons, injectedCopy.Order = []string{"inject:0"}, 1
	regular := roundRobinCandidate(1, 12, .1, "dense")
	regular.Order = 2
	newer := roundRobinCandidate(2, 21, .05, "dense")
	injectedCopy.Context.Priority, regular.Context.Priority, newer.Context.Priority = 1, 1, 1
	newer.Context.UpdatedAt = 100
	low := roundRobinCandidate(3, 31, .99, "dense")
	fallback := roundRobinCandidate(4, 41, .999, "friend")
	fallback.Context.CapturedNeed, fallback.Context.Priority = nil, 100
	got := Merge([]Candidate{low, regular, fallback, injectedCopy, newer}, []Kind{Broadcast}, Recommendation, 20, NeedAttributionRelative, everyTurn)
	require.Equal(t, []int64{21, 11, 12, 31, 41}, ids(Execution{Candidates: got}))
	require.True(t, injected(got[1]))
}

func TestNeedRelativeRoundRobinKeepsPageSourceCeiling(t *testing.T) {
	in := []Candidate{}
	for i := int64(1); i <= 10; i++ {
		in = append(in, roundRobinCandidate(1, 100+i, .9-float64(i)/100, "swing_i2i"),
			roundRobinCandidate(2, 200+i, .5-float64(i)/100, "dense"),
			roundRobinCandidate(3, 300+i, .3-float64(i)/100, "dense"))
	}
	merged := Merge(in, []Kind{Broadcast}, Recommendation, 200, NeedAttributionRelative, everyTurn)
	page, _ := SelectRecommendationPage(merged, 20, []SourceLimit{{Source: "swing_i2i", Numerator: 1, Denominator: 4}})
	require.Len(t, page, 20)
	swing := 0
	for _, c := range page {
		if slices.Contains(c.Document.Channels, "swing_i2i") {
			swing++
		}
	}
	require.Equal(t, 5, swing)
}

func TestNeedMixAlternatesReservedAndGlobalPositions(t *testing.T) {
	c := func(ctx, id int64, cosine, p float64) Candidate {
		x := attributionCandidate(ctx, cosine, p, "dense")
		x.Document.Ref.ID = id
		return x
	}
	in := []Candidate{
		c(1, 101, .7, .90), c(1, 102, .7, .89), c(1, 103, .7, .88), c(1, 104, .7, .87),
		c(2, 201, .3, .95), c(2, 202, .5, .20), c(2, 203, .5, .19),
		c(3, 301, .6, .10), c(3, 302, .6, .09),
	}
	mix := &NeedMix{Numerator: 3, Denominator: 4, MinNeedCosine: .4}
	// R R R G: below-floor 201 only enters through the global position; then exhausted Needs yield.
	got := Merge(in, []Kind{Broadcast}, Recommendation, 20, NeedAttributionRelative, mix)
	require.Equal(t, []int64{101, 202, 301, 201, 102, 203, 302, 103, 104}, ids(Execution{Candidates: got}))
	require.Equal(t, []int64{101, 202, 301, 201, 102}, ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 5, NeedAttributionRelative, mix)}))
	none := &NeedMix{Numerator: 3, Denominator: 4, MinNeedCosine: .99}
	require.Equal(t, ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 20, NeedAttributionRelative, nil)}),
		ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 20, NeedAttributionRelative, none)}), "no qualifying candidate keeps global order")

	missing := c(2, 204, 0, .99)
	missing.Score.Features = nil
	got = Merge(append(in, missing), []Kind{Broadcast}, Recommendation, 3, NeedAttributionRelative, &NeedMix{Numerator: 1, Denominator: 1, MinNeedCosine: -1})
	require.Equal(t, []int64{101, 201, 301}, ids(Execution{Candidates: got}), "a copy without current-Need cosine never takes a reserved position")
}

func TestNeedMixInjectedCopyKeepsReservedPosition(t *testing.T) {
	a := roundRobinCandidate(1, 11, .9, "dense")
	a.Score.Features["need_cosine"] = .7
	inj := roundRobinCandidate(2, 21, .01, "new_ugc_recall")
	inj.Score.Features["need_cosine"], inj.Reasons, inj.Order = .1, []string{"inject:0"}, 1
	b := roundRobinCandidate(2, 22, .5, "dense")
	b.Score.Features["need_cosine"], b.Order = .6, 2
	got := Merge([]Candidate{a, b, inj}, []Kind{Broadcast}, Recommendation, 20, NeedAttributionRelative, &NeedMix{Numerator: 1, Denominator: 1, MinNeedCosine: .4})
	require.Equal(t, []int64{11, 21, 22}, ids(Execution{Candidates: got}))
}

func TestNeedCosineFeatureIndexExists(t *testing.T) {
	require.GreaterOrEqual(t, needCosCtx0, 0)
}

func TestExperimentArmsRecordRunningExperimentsOnly(t *testing.T) {
	require.Nil(t, (&Engine{}).experimentArms(7))
	var control int64
	for control = 1; bucketEnabled(needRelativeSalt, control, 5000); control++ {
	}
	e := &Engine{NeedRelativeBPS: 5000, ProfileSemanticBPS: 10000, RatingNextBPS: 10000}
	require.Equal(t, map[string]string{"discovery-need-relative-v1": "control", "discovery-profile-semantic-v1": "treatment"}, e.experimentArms(control),
		"control is recorded; the rating challenger needs a running rating treatment")
	e = &Engine{RatingLRBPS: 10000, RatingNextBPS: 10000, ConsumedRefillBPS: 10000}
	require.Equal(t, map[string]string{"discovery-rating-lr-v1": "treatment", "discovery-rating-lr-v2": "treatment", "discovery-consumed-refill-v1": "treatment"}, e.experimentArms(control))
}

func TestRelativeAttributionUsesLocalRanks(t *testing.T) {
	makeCopy := func(ctx, id int64, cosine float64) Candidate {
		c := attributionCandidate(ctx, cosine, .5, "dense")
		c.Document.Ref.ID = id
		return c
	}
	broad := makeCopy(1, 10, .74)
	narrow := makeCopy(2, 10, .60)
	in := []Candidate{makeCopy(1, 11, .80), makeCopy(1, 12, .78), makeCopy(1, 13, .76), broad, narrow, makeCopy(2, 14, .50)}
	winner := func(in []Candidate, mode NeedAttribution) int64 {
		t.Helper()
		for _, c := range Merge(in, []Kind{Broadcast}, Recommendation, 20, mode, nil) {
			if c.Document.Ref.ID == 10 {
				return c.Context.ID
			}
		}
		t.Fatal("missing shared broadcast")
		return 0
	}
	require.EqualValues(t, 1, winner(in, NeedAttributionCosine))
	for i := 0; i < 2; i++ {
		require.EqualValues(t, 2, winner(in, NeedAttributionRelative))
		slices.Reverse(in)
	}
	narrow.Document.Channels = []string{"swing_i2i"}
	in[4] = narrow
	require.EqualValues(t, 1, winner(in, NeedAttributionRelative))
	narrow.Document.Channels = []string{"dense"}
	in[4] = narrow
	in[3].Reasons = []string{"inject:0"}
	require.EqualValues(t, 1, winner(in, NeedAttributionRelative))

	a := makeCopy(1, 10, .1)
	b := makeCopy(2, 10, .9)
	ignored := makeCopy(1, 99, 1)
	ignored.Score.Eligible = false
	in = []Candidate{a, b, ignored, makeCopy(2, 15, .9), makeCopy(2, 16, .4)}
	strength := localStrength(in)
	for _, c := range []Candidate{a, b, in[3]} {
		v, ok := strength(c)
		require.True(t, ok)
		require.Equal(t, 1.0, v)
	}
	for i := 0; i < 2; i++ {
		require.EqualValues(t, 1, winner(in, NeedAttributionRelative))
		slices.Reverse(in)
	}
	in[0].Score.Features = nil
	require.EqualValues(t, 2, winner(in, NeedAttributionRelative))
}

func TestRelativeBucketEnablesMixInRecommendations(t *testing.T) {
	e, _ := needFixture()
	e.NeedMix = everyTurn
	e.Policies = func(context.Context, []Candidate, Mode, int) ([]Candidate, error) {
		return []Candidate{roundRobinCandidate(1, 11, .9, "dense"), roundRobinCandidate(1, 12, .8, "dense"), roundRobinCandidate(2, 21, .1, "dense")}, nil
	}
	run := func(bps int, mode Mode) Execution {
		e.NeedRelativeBPS = bps
		r := Request{Limit: 2, SourceKinds: []Kind{Broadcast}}
		if mode == Search {
			r.Query = "design"
		}
		x, err := e.Execute(context.Background(), 1, r, mode, testNow)
		require.NoError(t, err)
		return x
	}
	require.Equal(t, []int64{11, 12}, ids(run(0, Recommendation)))
	treatment := run(10000, Recommendation)
	require.Equal(t, []int64{11, 21}, ids(treatment))
	require.Equal(t, "treatment", treatment.Experiments["discovery-need-relative-v1"])
	require.Equal(t, run(0, Search), run(10000, Search))
}

func TestCosineReferenceCapIsReplayOnly(t *testing.T) {
	e, source := needFixture()
	ctx := context.Background()
	kinds := []Kind{Broadcast}
	_, err := e.ReplayCosineReference(ctx, 1, e.frozenContexts, kinds, 20, testNow)
	require.NoError(t, err)
	require.Equal(t, []int{300, 300, 300}, source.batches)
	source.batches = nil
	_, err = e.ReplayContexts(ctx, 1, e.frozenContexts, kinds, 20, testNow)
	require.NoError(t, err)
	require.Equal(t, []int{240, 240, 240}, source.batches)
}
