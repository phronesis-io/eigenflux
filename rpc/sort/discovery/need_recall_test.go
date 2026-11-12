package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"eigenflux_server/pkg/metrics"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestNeedRecallQueriesAndCaps(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		bps, budget, cap, reads int
		mode                    Mode
		fail, timeout           bool
	}{
		{"off", 0, 0, 200, 0, Recommendation, false, false},
		{"search", 10000, 0, 200, 0, Search, false, false},
		{"on", 10000, 0, 240, 1, Recommendation, false, false},
		{"both", 10000, 10000, 300, 1, Recommendation, false, false},
		{"history_error", 10000, 0, 240, 1, Recommendation, true, false},
		{"history_timeout", 10000, 0, 240, 1, Recommendation, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, s := budgetFixture()
			e.NeedRecallBPS, e.NeedBudgetBPS = tc.bps, tc.budget
			s.timeout = tc.timeout
			if tc.fail {
				s.historyErr = errors.New("unavailable")
			}
			r := Request{SourceKinds: []Kind{Broadcast}, Limit: 20, Prefetch: true}
			if tc.mode == Search {
				r.Query = "design"
			}
			const now = 1800000000000
			x, err := e.Execute(context.Background(), 1, r, tc.mode, now)
			require.NoError(t, err)
			require.Equal(t, tc.reads, s.historyCalls)
			require.Equal(t, []int{tc.cap, tc.cap, tc.cap}, s.batches)
			require.Len(t, x.Candidates, 200)
			on := tc.reads > 0
			for id := 1; id <= 3; id++ {
				require.Contains(t, s.queries, fmt.Sprintf("%d:dense:80", id))
				key := fmt.Sprintf("%d:dense:40", id)
				if on {
					require.Contains(t, s.queries, key)
					q := s.queries[key]["knn"].(map[string]any)
					require.Equal(t, 40, q["k"])
					filter := q["filter"].(map[string]any)["bool"].(map[string]any)
					require.Contains(t, filter["filter"], map[string]any{"range": map[string]any{"created_at": map[string]any{"gte": time.UnixMilli(now - 7*day).UTC().Format(time.RFC3339Nano)}}})
				} else {
					require.NotContains(t, s.queries, key)
				}
			}
			for key, q := range s.queries {
				raw, err := json.Marshal(q)
				require.NoError(t, err)
				excluded := on && !tc.fail && !tc.timeout && strings.Contains(key, ":dense:")
				if excluded {
					filter := q["knn"].(map[string]any)["filter"].(map[string]any)["bool"].(map[string]any)
					require.Contains(t, filter["must_not"], terms("group_id", []int64{91, 92}))
				} else {
					require.NotContains(t, string(raw), `"group_id"`)
				}
				if !strings.HasSuffix(key, ":dense:40") {
					require.NotContains(t, string(raw), `"created_at"`)
				}
			}
			if on {
				require.Equal(t, "treatment", x.Experiments["discovery-need-recall-v1"])
			} else {
				require.NotContains(t, x.Experiments, "discovery-need-recall-v1")
			}
		})
	}
}

func TestNeedRecallOnlyVectorBroadcastNeeds(t *testing.T) {
	e, s := budgetFixture()
	e.NeedRecallBPS = 10000
	e.frozenContexts[0].Vector = nil
	e.frozenContexts[1].CapturedNeed = nil
	e.frozenContexts[1].Origin = "profile"
	e.frozenContexts[2].CapturedNeed = nil
	e.frozenContexts[2].Origin = "agent_context"
	_, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}}, Recommendation, 1800000000000)
	require.NoError(t, err)
	require.Equal(t, 0, s.historyCalls)
	require.Equal(t, []int{200, 200, 200}, s.batches)
	for key := range s.queries {
		require.NotContains(t, key, ":dense:40")
	}
	require.NotContains(t, s.queries, "1:dense:80")
}

func TestNeedRecallHistoryCancellation(t *testing.T) {
	e, s := budgetFixture()
	e.NeedRecallBPS = 10000
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.cancel, s.timeout = cancel, true
	_, err := e.Execute(ctx, 1, Request{SourceKinds: []Kind{Broadcast}}, Recommendation, 1800000000000)
	require.ErrorIs(t, err, context.Canceled)
}

func TestNeedRecallBuckets(t *testing.T) {
	cells := map[[2]bool]int{}
	for i := 0; i < 10000; i++ {
		hash := sha256.Sum256([]byte(fmt.Sprint(i)))
		owner := int64(binary.BigEndian.Uint64(hash[:8]) & ((1 << 63) - 1))
		on := bucketEnabled(needRecallSalt, owner, 5000)
		require.Equal(t, on, bucketEnabled(needRecallSalt, owner, 5000))
		require.False(t, bucketEnabled(needRecallSalt, owner, 0))
		require.True(t, bucketEnabled(needRecallSalt, owner, 10000))
		cells[[2]bool{on, bucketEnabled(needBudgetSalt, owner, 5000)}]++
	}
	for _, a := range []bool{false, true} {
		for _, b := range []bool{false, true} {
			require.InDelta(t, 2500, cells[[2]bool{a, b}], 400)
		}
	}
	e := Engine{NeedRecallBPS: 5000}
	for owner := int64(1); owner <= 20; owner++ {
		arm := "control"
		if bucketEnabled(needRecallSalt, owner, 5000) {
			arm = "treatment"
		}
		require.Equal(t, map[string]string{"discovery-need-recall-v1": arm}, e.experimentArms(owner))
	}
}

func TestNeedRecallHistogram(t *testing.T) {
	count := func(lane, exclusion string) uint64 {
		m := &dto.Metric{}
		h := metrics.DiscoveryNeedDenseDuration.WithLabelValues(lane, exclusion)
		require.NoError(t, h.(interface{ Write(*dto.Metric) error }).Write(m))
		return m.Histogram.GetSampleCount()
	}
	for _, fail := range []bool{false, true} {
		e, s := budgetFixture()
		e.NeedRecallBPS = 10000
		exclusion := "on"
		if fail {
			s.historyErr = errors.New("unavailable")
			exclusion = "off"
		}
		main, fresh := count("main", exclusion), count("fresh", exclusion)
		_, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}}, Recommendation, 1800000000000)
		require.NoError(t, err)
		require.Equal(t, main+3, count("main", exclusion))
		require.Equal(t, fresh+3, count("fresh", exclusion))
	}
}

// Distinct lanes force the merge cap to choose among competing candidates.
type distinctNeedLanes struct{ *needBudgetSource }

func (s distinctNeedLanes) Recall(ctx context.Context, c Context, k Kind, ch string, n int) ([]Document, error) {
	if _, err := s.needBudgetSource.Recall(ctx, c, k, ch, n); err != nil {
		return nil, err
	}
	offset := int64(channelOrder(ch) * 1000)
	if c.CreatedSince() > 0 {
		offset += 500
	}
	docs := make([]Document, n)
	for i := range docs {
		docs[i] = refillDoc(offset + int64(i) + 1)
	}
	return docs, nil
}
func TestNeedRecallDeterministicAtCap(t *testing.T) {
	var want []byte
	for i := 0; i < 20; i++ {
		e, s := budgetFixture()
		e.NeedRecallBPS = 10000
		e.Sources = distinctNeedLanes{s}
		x, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}, Prefetch: true}, Recommendation, 1800000000000)
		require.NoError(t, err)
		require.Equal(t, []int{240, 240, 240}, s.batches)
		got, err := json.Marshal(x)
		require.NoError(t, err)
		if i == 0 {
			want = got
		} else {
			require.Equal(t, string(want), string(got))
		}
	}
}

func TestNeedRecallCapRemainsLocalToEligibleContext(t *testing.T) {
	e, s := budgetFixture()
	e.NeedRecallBPS = 10000
	e.frozenContexts[1].Vector = nil
	e.frozenContexts[2].CapturedNeed = nil
	_, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}}, Recommendation, 1800000000000)
	require.NoError(t, err)
	require.Equal(t, []int{240, 200, 200}, s.batches)
	require.Equal(t, 1, s.historyCalls)
	require.Contains(t, s.queries, "1:dense:40")
	require.NotContains(t, s.queries, "2:dense:40")
	require.NotContains(t, s.queries, "3:dense:40")
}

func TestNeedRecallNonBroadcastDoesNotAddLane(t *testing.T) {
	e, s := budgetFixture()
	e.NeedRecallBPS = 10000
	for i := range e.frozenContexts {
		e.frozenContexts[i].Kinds = []Kind{Agent}
	}
	_, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Agent}}, Recommendation, 1800000000000)
	require.NoError(t, err)
	require.Equal(t, 0, s.historyCalls)
	for key := range s.queries {
		require.NotContains(t, key, ":dense:40")
	}
}

type distinctProfileLanes struct{ *profileFake }

func (s distinctProfileLanes) RecallProfile(_ context.Context, _ Context, _ []float32, since int64, n int) ([]Document, error) {
	offset := int64(1000)
	if since > 0 {
		offset = 2000
	}
	docs := make([]Document, n)
	for i := range docs {
		docs[i] = profileDoc(offset+int64(i), 5, 1)
	}
	return docs, nil
}
func TestProfileDenseLaneOrderIsDeterministicWithNeedRecallOff(t *testing.T) {
	var want []byte
	for i := 0; i < 20; i++ {
		e, s := profileFixture()
		e.ProfileSemanticBPS = 10000
		e.frozenContexts = []Context{{ID: 1, OwnerID: 1, State: "active", Origin: "agent_context", Query: "design", Kinds: []Kind{Broadcast}}}
		for id := int64(1); id <= 300; id++ {
			s.docs = append(s.docs, profileDoc(id, 5, 1))
		}
		e.Sources = distinctProfileLanes{s}
		x, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}, Prefetch: true}, Recommendation, testNow)
		require.NoError(t, err)
		got, err := json.Marshal(x)
		require.NoError(t, err)
		if i == 0 {
			want = got
		} else {
			require.Equal(t, string(want), string(got))
		}
	}
}

// slotProbe reports whether six per-context recalls can run concurrently while
// the shared history read is in progress; a caller holding a slot caps this at
// five. Shared lanes (Swing) are not held, since their waiters also take slots.
type slotProbe struct {
	*needBudgetSource
	mu       sync.Mutex
	inFlight int
	released chan struct{}
	timedOut bool
}

func (p *slotProbe) ConsumedGroups(ctx context.Context, owner int64) ([]int64, bool, error) {
	defer close(p.released)
	for {
		p.mu.Lock()
		n := p.inFlight
		p.mu.Unlock()
		if n >= 6 {
			return p.needBudgetSource.ConsumedGroups(ctx, owner)
		}
		select {
		case <-ctx.Done():
			p.timedOut = true
			return nil, false, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}
func (p *slotProbe) Recall(ctx context.Context, c Context, k Kind, ch string, n int) ([]Document, error) {
	if ch == "lexical" || strings.HasSuffix(ch, "_recall") {
		p.mu.Lock()
		p.inFlight++
		p.mu.Unlock()
		select {
		case <-p.released:
		case <-time.After(2 * time.Second):
		}
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}
	return p.needBudgetSource.Recall(ctx, c, k, ch, n)
}
func TestNeedRecallHistoryDoesNotHoldRecallSlots(t *testing.T) {
	e, s := budgetFixture()
	e.NeedRecallBPS = 10000
	p := &slotProbe{needBudgetSource: s, released: make(chan struct{})}
	e.Sources = p
	_, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}}, Recommendation, 1800000000000)
	require.NoError(t, err)
	require.False(t, p.timedOut, "a history wait held a recall slot")
	require.Equal(t, 1, s.historyCalls)
	q := s.queries["1:dense:40"]["knn"].(map[string]any)["filter"].(map[string]any)["bool"].(map[string]any)
	require.Contains(t, q["must_not"], terms("group_id", []int64{91, 92}))
}

type laneCounter struct {
	distinctNeedLanes
	inputs [][]int64
}

func (s *laneCounter) Hydrate(ctx context.Context, owner int64, m Mode, docs []Document) ([]Document, error) {
	ids := make([]int64, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.Ref.ID)
	}
	s.inputs = append(s.inputs, ids)
	return s.distinctNeedLanes.Hydrate(ctx, owner, m, docs)
}
func TestNeedRecallFreshLaneIsAdditiveAtCap(t *testing.T) {
	lane := func(id int64) string {
		switch {
		case id >= 1501 && id < 2000:
			return "need_fresh"
		case id >= 1000 && id < 1500:
			return "dense"
		}
		for _, ch := range []string{"lexical", "dense", "hot_recall", "new_recall", "new_ugc_recall", "friend", "swing_i2i"} {
			if int64(channelOrder(ch)) == id/1000 {
				return ch
			}
		}
		return "?"
	}
	counts := func(bps int) []map[string]int {
		e, s := budgetFixture()
		e.NeedRecallBPS = bps
		lc := &laneCounter{distinctNeedLanes: distinctNeedLanes{s}}
		e.Sources = lc
		_, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}, Prefetch: true}, Recommendation, 1800000000000)
		require.NoError(t, err)
		out := []map[string]int{}
		for _, ids := range lc.inputs {
			m := map[string]int{}
			for _, id := range ids {
				m[lane(id)]++
			}
			out = append(out, m)
		}
		return out
	}
	off, on := counts(0), counts(10000)
	require.Len(t, off, 3)
	require.Len(t, on, 3)
	for i := range on {
		require.Equal(t, needFreshLimit, on[i]["need_fresh"])
		require.Zero(t, off[i]["need_fresh"])
		for ch, n := range off[i] {
			require.GreaterOrEqual(t, on[i][ch], n, "context %d lane %s lost positions to the fresh lane", i+1, ch)
		}
	}
}
