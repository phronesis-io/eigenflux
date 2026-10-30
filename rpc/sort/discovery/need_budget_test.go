package discovery

import (
	"context"
	"encoding/json"
	"errors"
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

func TestNeedBudgetAttributionBeforeRatingOrder(t *testing.T) {
	low := attributionCandidate(1, .3, .9, "swing_i2i")
	mid := attributionCandidate(2, .5, .8, "swing_i2i")
	high := attributionCandidate(3, .7, .7, "swing_i2i")
	for _, reverse := range []bool{false, true} {
		in := []Candidate{low, mid, high}
		if reverse {
			slices.Reverse(in)
		}
		control := Merge(in, []Kind{Broadcast}, Recommendation, 20, false, nil)
		require.Equal(t, int64(1), control[0].Context.ID)
		got := Merge(in, []Kind{Broadcast}, Recommendation, 20, true, nil)
		require.Len(t, got, 1)
		require.Equal(t, int64(3), got[0].Context.ID)
		require.Equal(t, .7, got[0].FinalScore, "winning copy retains its LR score")
		require.Equal(t, Merge(in, []Kind{Broadcast}, Search, 20, false, nil), Merge(in, []Kind{Broadcast}, Search, 20, true, nil))
	}
	mid.Document.Channels = []string{"dense"}
	got := Merge([]Candidate{low, mid, high}, []Kind{Broadcast}, Recommendation, 20, true, nil)
	require.Equal(t, int64(2), got[0].Context.ID, "own recall outranks pooled cosine")
	require.ElementsMatch(t, []string{"dense", "swing_i2i"}, got[0].Document.Channels, "source caps retain union attribution")
	require.Equal(t, []string{"dense"}, mid.Document.Channels, "input provenance is not mutated")
	low.Reasons = []string{"inject:0"}
	got = Merge([]Candidate{mid, high, low}, []Kind{Broadcast}, Recommendation, 20, true, nil)
	require.Equal(t, int64(1), got[0].Context.ID, "injection must survive ownership")
	require.True(t, injected(got[0]))
	low.Score.Eligible = false
	got = Merge([]Candidate{low, mid, high}, []Kind{Broadcast}, Recommendation, 20, true, nil)
	require.Equal(t, int64(2), got[0].Context.ID, "ineligible injection cannot discard a valid copy")
}

func TestNeedBudgetAttributionMissingAndTiedCosine(t *testing.T) {
	a, b := attributionCandidate(1, .4, .8, "dense"), attributionCandidate(2, .4, .9, "dense")
	got := Merge([]Candidate{a, b}, []Kind{Broadcast}, Recommendation, 20, true, nil)
	require.Equal(t, int64(2), got[0].Context.ID, "ties preserve existing LR order")
	a.Score.Features, b.Score.Features = nil, nil
	a.RatingFeatures = make([]float64, len(discoverylr.RatingFeatureNames))
	b.RatingFeatures = make([]float64, len(discoverylr.RatingFeatureNames))
	i := slices.Index(discoverylr.RatingFeatureNames, "need_cos_ctx0")
	a.RatingFeatures[i], b.RatingFeatures[i] = .8, .3
	got = Merge([]Candidate{a, b}, []Kind{Broadcast}, Recommendation, 20, true, nil)
	require.Equal(t, int64(1), got[0].Context.ID, "rating evidence works without the semantic experiment")
	a.RatingFeatures, b.RatingFeatures = nil, nil
	got = Merge([]Candidate{a, b}, []Kind{Broadcast}, Recommendation, 20, true, nil)
	require.Equal(t, int64(2), got[0].Context.ID, "missing evidence preserves existing order")
}

type needBudgetSource struct {
	*refillFake
	mu      sync.Mutex
	queries map[string]map[string]any
	batches []int
	timeout bool
	cancel  context.CancelFunc
}

func (s *needBudgetSource) ConsumedGroups(ctx context.Context, owner int64) ([]int64, bool, error) {
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
func (s *needBudgetSource) Recall(ctx context.Context, c Context, k Kind, ch string, n int) ([]Document, error) {
	if ch == "lexical" || ch == "dense" {
		q, err := Query(c, k, ch, n)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.queries[fmt.Sprintf("%d:%s", c.ID, ch)] = q
		s.mu.Unlock()
	}
	return s.sourceFake.Recall(ctx, c, k, ch, n)
}
func (s *needBudgetSource) Hydrate(ctx context.Context, owner int64, m Mode, docs []Document) ([]Document, error) {
	s.batches = append(s.batches, len(docs))
	return s.sourceFake.Hydrate(ctx, owner, m, docs)
}
func budgetFixture() (*Engine, *needBudgetSource) {
	e, f, _ := engineFixture()
	s := &needBudgetSource{refillFake: &refillFake{sourceFake: f, groups: []int64{91, 92}}, queries: map[string]map[string]any{}}
	e.Sources = s
	for id := int64(1); id <= 3; id++ {
		e.frozenContexts = append(e.frozenContexts, Context{ID: id, OwnerID: 1, State: "active", Origin: "need_input", Query: "design", Vector: []float32{1, 0}, Kinds: []Kind{Broadcast}, CapturedNeed: &need.Snapshot{InputID: id}})
	}
	for id := int64(1); id <= 320; id++ {
		f.docs = append(f.docs, refillDoc(id))
	}
	return e, s
}
func TestNeedBudgetSharedHistoryAndMergeCap(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		bps, wantCap, wantReads int
		mode                    Mode
		fail, timeout           bool
	}{
		{"off", 0, 200, 0, Recommendation, false, false},
		{"on", 10000, 300, 1, Recommendation, false, false},
		{"search", 10000, 200, 0, Search, false, false},
		{"history_error", 10000, 300, 1, Recommendation, true, false},
		{"history_timeout", 10000, 300, 1, Recommendation, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, s := budgetFixture()
			e.NeedBudgetBPS = tc.bps
			s.timeout = tc.timeout
			if tc.fail {
				s.historyErr = errors.New("unavailable")
			}
			r := Request{SourceKinds: []Kind{Broadcast}, Limit: 20, Prefetch: true}
			if tc.mode == Search {
				r.Query = "design"
			}
			x, err := e.Execute(context.Background(), 1, r, tc.mode, 1000)
			require.NoError(t, err)
			require.Equal(t, tc.wantReads, s.historyCalls)
			require.Equal(t, []int{tc.wantCap, tc.wantCap, tc.wantCap}, s.batches)
			require.Len(t, x.Candidates, 200, "global snapshot cap stays 200")
			var arms map[string]string
			if tc.bps > 0 && tc.mode == Recommendation {
				arms = map[string]string{"discovery-need-budget-v1": "treatment"}
			}
			require.Equal(t, arms, x.Experiments)
			for key, q := range s.queries {
				raw, err := json.Marshal(q)
				require.NoError(t, err)
				excluded := tc.wantReads > 0 && !tc.fail && !tc.timeout && key[len(key)-5:] == "dense"
				if excluded {
					filter := q["knn"].(map[string]any)["filter"].(map[string]any)["bool"].(map[string]any)
					require.Contains(t, filter["must_not"], terms("group_id", []int64{91, 92}))
				} else {
					require.NotContains(t, string(raw), "\"group_id\"")
				}
			}
		})
	}
}
func TestNeedBudgetHistoryCancellationPropagates(t *testing.T) {
	e, s := budgetFixture()
	e.NeedBudgetBPS = 10000
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.cancel = cancel
	s.timeout = true
	_, err := e.Execute(ctx, 1, Request{SourceKinds: []Kind{Broadcast}}, Recommendation, 1000)
	require.ErrorIs(t, err, context.Canceled)
}
func TestNeedBudgetBucketStableAndBounded(t *testing.T) {
	on := 0
	for owner := int64(1); owner <= 1000; owner++ {
		got := bucketEnabled("discovery-need-budget-v1:", owner, 5000)
		require.Equal(t, got, bucketEnabled("discovery-need-budget-v1:", owner, 5000))
		if got {
			on++
		}
		require.False(t, bucketEnabled("discovery-need-budget-v1:", owner, 0))
		require.True(t, bucketEnabled("discovery-need-budget-v1:", owner, 10000))
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

func TestNeedBudgetRoundRobinPreservesLocalRanking(t *testing.T) {
	in := []Candidate{
		roundRobinCandidate(1, 101, .9, "dense"), roundRobinCandidate(1, 102, .8, "dense"), roundRobinCandidate(1, 103, .7, "dense"),
		roundRobinCandidate(2, 201, .2, "dense"), roundRobinCandidate(2, 202, .1, "dense"),
		roundRobinCandidate(3, 301, .6, "dense"), roundRobinCandidate(3, 302, .5, "dense"),
	}
	for _, reverse := range []bool{false, true} {
		if reverse {
			slices.Reverse(in)
		}
		got := Merge(in, []Kind{Broadcast}, Recommendation, 20, true, everyTurn)
		require.Equal(t, []int64{101, 201, 301, 102, 202, 302, 103}, ids(Execution{Candidates: got}))
		require.Equal(t, []int64{101, 201, 301, 102}, ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 4, true, everyTurn)}))
		for _, c := range got {
			require.Equal(t, c.LR.Probability, c.FinalScore, "mixing must not rewrite scores")
		}
		require.Equal(t, []int64{101, 102, 103, 301, 302, 201, 202}, ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 20, false, nil)}))
		require.Equal(t, []int64{101, 102, 103, 301, 302, 201, 202}, ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 20, true, nil)}), "no need_mix keeps global order")
		require.Equal(t, Merge(in, []Kind{Broadcast}, Recommendation, 20, false, nil), Merge(in, []Kind{Broadcast}, Recommendation, 20, false, everyTurn), "mixing needs the Need budget bucket")
		require.Equal(t, Merge(in, []Kind{Broadcast}, Search, 20, false, nil), Merge(in, []Kind{Broadcast}, Search, 20, true, everyTurn))
	}
}

func TestNeedBudgetRoundRobinSkipsDuplicatesWithinTurn(t *testing.T) {
	in := []Candidate{
		roundRobinCandidate(1, 11, .9, "dense"), roundRobinCandidate(1, 12, .8, "dense"), roundRobinCandidate(1, 13, .7, "dense"),
		roundRobinCandidate(2, 21, .3, "dense"), roundRobinCandidate(2, 22, .2, "dense"), roundRobinCandidate(2, 23, .1, "dense"),
	}
	in[0].Document.GroupID, in[3].Document.GroupID = 50, 50
	in[4].Score.Eligible = false
	got := Merge(in, []Kind{Broadcast}, Recommendation, 20, true, everyTurn)
	require.Equal(t, []int64{11, 23, 12, 13}, ids(Execution{Candidates: got}), "duplicate and ineligible candidates do not consume a Need's turn")
}

func TestNeedBudgetRoundRobinPreservesPriorityFallbackAndInjection(t *testing.T) {
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
	got := Merge([]Candidate{low, regular, fallback, injectedCopy, newer}, []Kind{Broadcast}, Recommendation, 20, true, everyTurn)
	require.Equal(t, []int64{21, 11, 12, 31, 41}, ids(Execution{Candidates: got}))
	require.True(t, injected(got[1]))
}

func TestNeedBudgetRoundRobinKeepsPageSourceCeiling(t *testing.T) {
	in := []Candidate{}
	for i := int64(1); i <= 10; i++ {
		in = append(in, roundRobinCandidate(1, 100+i, .9-float64(i)/100, "swing_i2i"),
			roundRobinCandidate(2, 200+i, .5-float64(i)/100, "dense"),
			roundRobinCandidate(3, 300+i, .3-float64(i)/100, "dense"))
	}
	merged := Merge(in, []Kind{Broadcast}, Recommendation, 200, true, everyTurn)
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
	got := Merge(in, []Kind{Broadcast}, Recommendation, 20, true, mix)
	require.Equal(t, []int64{101, 202, 301, 201, 102, 203, 302, 103, 104}, ids(Execution{Candidates: got}))
	require.Equal(t, []int64{101, 202, 301, 201, 102}, ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 5, true, mix)}))
	none := &NeedMix{Numerator: 3, Denominator: 4, MinNeedCosine: .99}
	require.Equal(t, ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 20, true, nil)}),
		ids(Execution{Candidates: Merge(in, []Kind{Broadcast}, Recommendation, 20, true, none)}), "no qualifying candidate keeps global order")

	missing := c(2, 204, 0, .99)
	missing.Score.Features = nil
	got = Merge(append(in, missing), []Kind{Broadcast}, Recommendation, 3, true, &NeedMix{Numerator: 1, Denominator: 1, MinNeedCosine: -1})
	require.Equal(t, []int64{101, 201, 301}, ids(Execution{Candidates: got}), "a copy without current-Need cosine never takes a reserved position")
}

func TestNeedMixInjectedCopyKeepsReservedPosition(t *testing.T) {
	a := roundRobinCandidate(1, 11, .9, "dense")
	a.Score.Features["need_cosine"] = .7
	inj := roundRobinCandidate(2, 21, .01, "new_ugc_recall")
	inj.Score.Features["need_cosine"], inj.Reasons, inj.Order = .1, []string{"inject:0"}, 1
	b := roundRobinCandidate(2, 22, .5, "dense")
	b.Score.Features["need_cosine"], b.Order = .6, 2
	got := Merge([]Candidate{a, b, inj}, []Kind{Broadcast}, Recommendation, 20, true, &NeedMix{Numerator: 1, Denominator: 1, MinNeedCosine: .4})
	require.Equal(t, []int64{11, 21, 22}, ids(Execution{Candidates: got}))
}

func TestNeedCosineFeatureIndexExists(t *testing.T) {
	require.GreaterOrEqual(t, needCosCtx0, 0)
}

func TestExperimentArmsRecordRunningExperimentsOnly(t *testing.T) {
	require.Nil(t, (&Engine{}).experimentArms(7))
	var control int64
	for control = 1; bucketEnabled(needBudgetSalt, control, 5000); control++ {
	}
	e := &Engine{NeedBudgetBPS: 5000, ProfileSemanticBPS: 10000, RatingNextBPS: 10000}
	require.Equal(t, map[string]string{"discovery-need-budget-v1": "control", "discovery-profile-semantic-v1": "treatment"}, e.experimentArms(control),
		"control is recorded; the rating challenger needs a running rating treatment")
	e = &Engine{RatingLRBPS: 10000, RatingNextBPS: 10000, ConsumedRefillBPS: 10000}
	require.Equal(t, map[string]string{"discovery-rating-lr-v1": "treatment", "discovery-rating-lr-v2": "treatment", "discovery-consumed-refill-v1": "treatment"}, e.experimentArms(control))
}
