package discovery

import (
	"context"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/recallsource"
	dto "github.com/prometheus/client_model/go"
	"testing"
)

func feedCount(source string) float64 {
	m := &dto.Metric{}
	_ = metrics.RecallFeedTotal.WithLabelValues(source).Write(m)
	return m.GetCounter().GetValue()
}

func TestRecommendationRetainsRecallMetrics(t *testing.T) {
	e, s, _ := engineFixture()
	s.owner.Clauses = []string{"design", "software"}
	for _, kind := range AllKinds {
		s.docs = append(s.docs, Document{Ref: SourceRef{kind, 9}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10})
	}
	// Recalled but already seen: counts in candidates, never in impressions.
	s.seen["broadcast:9"] = true
	labels := []string{"keyword", "hot_recall", "new_recall", "new_ugc_recall"}
	before := map[string]float64{}
	for _, label := range labels {
		before[label] = feedCount(label)
	}
	x, err := e.Execute(context.Background(), 1, Request{}, Recommendation, 100)
	if err != nil || len(x.Candidates) != 2 {
		t.Fatalf("execution: %+v %v", x, err)
	}
	for _, label := range labels {
		if got := feedCount(label) - before[label]; got != 1 {
			t.Errorf("%s candidate count = %v, want 1 across kinds and contexts", label, got)
		}
	}
	for _, label := range labels {
		before[label] = feedCount(label)
	}
	_, err = e.Execute(context.Background(), 1, Request{Query: "design"}, Search, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range labels {
		if feedCount(label) != before[label] {
			t.Errorf("search changed feed metric %s", label)
		}
	}
}

func TestRecallSourcesPreservesLabels(t *testing.T) {
	d := Document{Ref: SourceRef{Broadcast, 1}, Channels: []string{"lexical", "dense", "hot_recall", "new_recall", "new_ugc_recall", "lexical", "unknown"}}
	want := recallsource.Keyword | recallsource.KNN | recallsource.HotRecall | recallsource.NewRecall | recallsource.NewUGC
	if got := d.RecallSources(); got != want {
		t.Fatalf("sources = %x, want keyword|knn|hot|new|new_ugc", got)
	}
	for _, kind := range []Kind{Agent, Commission} {
		d.Ref.Type = kind
		if d.RecallSources() != 0 {
			t.Fatalf("%s counted as broadcast", kind)
		}
	}
}
