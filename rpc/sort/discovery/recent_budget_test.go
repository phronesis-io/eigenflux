package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/pkg/es"
	elasticsearch "github.com/elastic/go-elasticsearch/v8"
	"github.com/stretchr/testify/require"
)

func recentBudgetFixture(t *testing.T) (*Source, <-chan struct{}, *atomic.Int32) {
	t.Helper()
	started := make(chan struct{}, 100)
	active := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		var query map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&query)
		}
		if _, recent := query["sort"]; recent {
			active.Add(1)
			defer active.Add(-1)
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"_shards":{"failed":0},"hits":{"hits":[]}}`))
	}))
	previous := es.Client
	client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{server.URL}, DisableRetry: true})
	require.NoError(t, err)
	es.Client = client
	t.Cleanup(func() { es.Client = previous; server.Close() })
	return &Source{BroadcastIndex: "fixture"}, started, active
}

func TestRecentRecallBoundsConcurrentRequestsAndReleasesCanceledSlots(t *testing.T) {
	source, started, active := recentBudgetFixture(t)
	c := Context{Query: "design", Filters: Filters{ExcludeAuthors: []string{"1"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := source.Recall(ctx, c, Broadcast, "lexical_recent", 20)
			results <- err
		}()
	}
	for i := 0; i < recentRecallConcurrency; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("recall failed to acquire capacity")
		}
	}
	require.EqualValues(t, recentRecallConcurrency, active.Load())
	// Ordinary lexical recall remains available while the entire recent pool is occupied.
	ordinary, done := context.WithTimeout(context.Background(), 200*time.Millisecond)
	_, err := source.Recall(ordinary, c, Broadcast, "lexical", 80)
	done()
	require.NoError(t, err)
	cancel()
	wg.Wait()
	close(results)
	for err := range results {
		require.ErrorIs(t, err, context.Canceled)
	}
	require.Empty(t, source.recentSlots, "canceling holders and waiters must not leak capacity")
	require.Eventually(t, func() bool { return active.Load() == 0 }, time.Second, 10*time.Millisecond)
	require.Len(t, started, 0, "no queued call may reach ES after cancellation")
}

func TestRecentRecallDeadlineBoundsSlowSearchAndAdmission(t *testing.T) {
	source, _, active := recentBudgetFixture(t)
	c := Context{Query: "design", Filters: Filters{ExcludeAuthors: []string{"1"}}}
	started := time.Now()
	_, err := source.Recall(context.Background(), c, Broadcast, "lexical_recent", 20)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), recentRecallBudget+500*time.Millisecond)
	require.Empty(t, source.recentSlots)
	require.Eventually(t, func() bool { return active.Load() == 0 }, time.Second, 10*time.Millisecond)
	// The same deadline also bounds waiting for admission, without issuing ES work.
	for i := 0; i < recentRecallConcurrency; i++ {
		source.recentSlots <- struct{}{}
	}
	started = time.Now()
	_, err = source.Recall(context.Background(), c, Broadcast, "lexical_recent", 20)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), recentRecallBudget+500*time.Millisecond)
	require.Len(t, source.recentSlots, recentRecallConcurrency)
}

type recentEngineProbe struct {
	*sourceFake
	ordinary      chan struct{}
	recentStarted chan struct{}
	recentActive  atomic.Int32
	recentPeak    atomic.Int32
}

func (s *recentEngineProbe) Recall(ctx context.Context, c Context, k Kind, channel string, limit int) ([]Document, error) {
	if channel == "lexical_recent" {
		n := s.recentActive.Add(1)
		defer s.recentActive.Add(-1)
		for {
			p := s.recentPeak.Load()
			if n <= p || s.recentPeak.CompareAndSwap(p, n) {
				break
			}
		}
		s.recentStarted <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if channel == "lexical" {
		s.ordinary <- struct{}{}
	}
	return s.sourceFake.Recall(ctx, c, k, channel, limit)
}

func TestRecentAdmissionDoesNotOccupyOrdinaryEngineSlots(t *testing.T) {
	e, fake, store := engineFixture()
	for id := int64(1); id <= 5; id++ {
		store.active = append(store.active, capturedFixture(id, Broadcast))
	}
	probe := &recentEngineProbe{sourceFake: fake, ordinary: make(chan struct{}, 5), recentStarted: make(chan struct{}, 5)}
	e.Sources = probe
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := e.Execute(ctx, 1, Request{SourceKinds: []Kind{Broadcast}}, Recommendation, 100)
		result <- err
	}()
	select {
	case <-probe.recentStarted:
	case <-time.After(time.Second):
		t.Fatal("recent recall did not start")
	}
	for i := 0; i < 5; i++ {
		select {
		case <-probe.ordinary:
		case <-time.After(time.Second):
			t.Fatal("recent waiters blocked ordinary recall")
		}
	}
	require.EqualValues(t, 1, probe.recentPeak.Load(), "one request may hold at most one recent slot")
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	require.Zero(t, probe.recentActive.Load())
}
