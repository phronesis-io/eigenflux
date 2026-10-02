package delivery

import (
	"context"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/rpc/sort/discovery"
	dto "github.com/prometheus/client_model/go"
	"testing"
)

func impressionCount() float64 {
	m := &dto.Metric{}
	_ = metrics.RecallImpressionTotal.WithLabelValues("keyword").Write(m)
	return m.GetCounter().GetValue()
}

func TestRecommendationRetainsImpressionMetrics(t *testing.T) {
	s, e, _ := setup(t)
	for i := range e.x.Candidates {
		e.x.Candidates[i].Document.Channels = []string{"lexical", "lexical"}
	}
	before := impressionCount()
	for i := 0; i < 2; i++ {
		if _, err := s.Serve(context.Background(), 1, discovery.Request{}, discovery.Recommendation, "same"); err != nil {
			t.Fatal(err)
		}
	}
	if got := impressionCount() - before; got != 1 {
		t.Fatalf("impressions = %v, want 1 broadcast without counting retries", got)
	}
	if _, err := s.Serve(context.Background(), 1, discovery.Request{Query: "design"}, discovery.Search, ""); err != nil {
		t.Fatal(err)
	}
	if got := impressionCount() - before; got != 1 {
		t.Fatalf("search changed feed impressions: %v", got)
	}
}

func TestLegacyPagesCountOnlyDeliveredRecallImpressions(t *testing.T) {
	s, e, _ := setup(t)
	base := e.x.Candidates[0]
	base.Document.Channels = []string{"lexical"}
	e.x.Candidates = nil
	for i := int64(0); i < 3; i++ {
		c := base
		c.Document.Ref.ID += i
		e.x.Candidates = append(e.x.Candidates, c)
	}
	before := impressionCount()
	prepare := func(_ context.Context, x *discovery.Execution) error {
		if x.Candidates[0].Document.Ref.ID == base.Document.Ref.ID {
			x.Candidates = nil
		}
		return nil
	}
	for i, action := range []string{"refresh", "load_more", "load_more"} {
		_, _, err := s.ServePage(context.Background(), 1, action, 1, prepare)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := impressionCount()-before, float64(min(i+1, 2)); got != want {
			t.Fatalf("%s impressions = %v, want %v", action, got, want)
		}
	}
}
