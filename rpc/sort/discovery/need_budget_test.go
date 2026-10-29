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
		control := Merge(in, []Kind{Broadcast}, Recommendation, 20, false)
		require.Equal(t, int64(1), control[0].Context.ID)
		got := Merge(in, []Kind{Broadcast}, Recommendation, 20, true)
		require.Len(t, got, 1)
		require.Equal(t, int64(3), got[0].Context.ID)
		require.Equal(t, .7, got[0].FinalScore, "winning copy retains its LR score")
		require.Equal(t, Merge(in, []Kind{Broadcast}, Search, 20, false), Merge(in, []Kind{Broadcast}, Search, 20, true))
	}
	mid.Document.Channels = []string{"dense"}
	got := Merge([]Candidate{low, mid, high}, []Kind{Broadcast}, Recommendation, 20, true)
	require.Equal(t, int64(2), got[0].Context.ID, "own recall outranks pooled cosine")
	require.ElementsMatch(t, []string{"dense", "swing_i2i"}, got[0].Document.Channels, "source caps retain union attribution")
	require.Equal(t, []string{"dense"}, mid.Document.Channels, "input provenance is not mutated")
	low.Reasons = []string{"inject:0"}
	got = Merge([]Candidate{mid, high, low}, []Kind{Broadcast}, Recommendation, 20, true)
	require.Equal(t, int64(1), got[0].Context.ID, "injection must survive ownership")
	require.True(t, injected(got[0]))
	low.Score.Eligible = false
	got = Merge([]Candidate{low, mid, high}, []Kind{Broadcast}, Recommendation, 20, true)
	require.Equal(t, int64(2), got[0].Context.ID, "ineligible injection cannot discard a valid copy")
}

func TestNeedBudgetAttributionMissingAndTiedCosine(t *testing.T) {
	a, b := attributionCandidate(1, .4, .8, "dense"), attributionCandidate(2, .4, .9, "dense")
	got := Merge([]Candidate{a, b}, []Kind{Broadcast}, Recommendation, 20, true)
	require.Equal(t, int64(2), got[0].Context.ID, "ties preserve existing LR order")
	a.Score.Features, b.Score.Features = nil, nil
	a.RatingFeatures = make([]float64, len(discoverylr.RatingFeatureNames))
	b.RatingFeatures = make([]float64, len(discoverylr.RatingFeatureNames))
	i := slices.Index(discoverylr.RatingFeatureNames, "need_cos_ctx0")
	a.RatingFeatures[i], b.RatingFeatures[i] = .8, .3
	got = Merge([]Candidate{a, b}, []Kind{Broadcast}, Recommendation, 20, true)
	require.Equal(t, int64(1), got[0].Context.ID, "rating evidence works without the semantic experiment")
	a.RatingFeatures, b.RatingFeatures = nil, nil
	got = Merge([]Candidate{a, b}, []Kind{Broadcast}, Recommendation, 20, true)
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
		name                         string
		bps, cap, wantCap, wantReads int
		mode                         Mode
		fail, timeout                bool
	}{
		{"off", 0, 300, 200, 0, Recommendation, false, false},
		{"on", 10000, 0, 300, 1, Recommendation, false, false},
		{"explicit", 10000, 275, 275, 1, Recommendation, false, false},
		{"search", 10000, 300, 200, 0, Search, false, false},
		{"history_error", 10000, 0, 300, 1, Recommendation, true, false},
		{"history_timeout", 10000, 0, 300, 1, Recommendation, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, s := budgetFixture()
			e.NeedBudgetBPS = tc.bps
			e.ContextCandidateLimit = tc.cap
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
