package consumer

import (
	"context"
	"eigenflux_server/pkg/commissionindex"
	"errors"
	"testing"
)

type commissionTestSource struct {
	catalogue       commissionindex.CatalogueSnapshot
	statistics      commissionindex.StatisticsSnapshot
	err             error
	forbidCatalogue bool
}

func (s commissionTestSource) GetIndexSnapshot(context.Context, int64) (commissionindex.CatalogueSnapshot, error) {
	if s.forbidCatalogue {
		panic("statistics event must not read catalogue")
	}
	return s.catalogue, s.err
}
func (s commissionTestSource) ListActiveIndexSnapshots(context.Context, int64, int) ([]commissionindex.CatalogueSnapshot, int64, error) {
	return nil, 0, s.err
}
func (s commissionTestSource) GetStatistics(context.Context, int64) (commissionindex.StatisticsSnapshot, error) {
	return s.statistics, s.err
}
func (s commissionTestSource) BatchGetStatistics(context.Context, []int64) ([]commissionindex.StatisticsSnapshot, error) {
	return nil, s.err
}

type commissionTestStore struct {
	document   commissionindex.Document
	statistics commissionindex.StatisticsSnapshot
	err        error
}

func (s *commissionTestStore) Upsert(_ context.Context, d commissionindex.Document) error {
	s.document = d
	return s.err
}
func (s *commissionTestStore) UpsertStatistics(_ context.Context, v commissionindex.StatisticsSnapshot) error {
	s.statistics = v
	return s.err
}
func (s *commissionTestStore) Search(context.Context, commissionindex.SearchRequest) ([]commissionindex.Hit, error) {
	return nil, nil
}

type commissionTestEmbedder struct{}

func (commissionTestEmbedder) GetEmbedding(context.Context, string) ([]float32, error) {
	return []float32{1}, nil
}
func TestCommissionConsumerProjectsActiveSnapshot(t *testing.T) {
	store := &commissionTestStore{}
	c := &CommissionIndexConsumer{source: commissionTestSource{catalogue: commissionindex.CatalogueSnapshot{CommissionID: 4, Status: "active", CatalogueVersion: 2, Title: "Write"}, statistics: commissionindex.StatisticsSnapshot{CommissionID: 4, StatisticsVersion: 3}}, store: store, embedder: commissionTestEmbedder{}}
	result := c.Handle(context.Background(), "1-0", map[string]any{"event_id": "1", "schema_version": "1", "topic": commissionPublishedTopic, "aggregate_type": "commission", "aggregate_id": "4", "aggregate_version": "2", "occurred_at": "1", "payload_json": "{}"})
	if result != HandleSuccess || !store.document.Active || store.document.CatalogueVersion != 2 || store.document.StatisticsVersion != 3 {
		t.Fatalf("unexpected projection: %#v, %v", store.document, result)
	}
}

func TestCommissionConsumerProjectsStatisticsEnvelope(t *testing.T) {
	store := &commissionTestStore{}
	c := &CommissionIndexConsumer{
		source: commissionTestSource{
			forbidCatalogue: true,
			statistics:      commissionindex.StatisticsSnapshot{CommissionID: 4, StatisticsVersion: 7},
		},
		store: store, embedder: nil,
	}
	result := c.Handle(context.Background(), "2-0", map[string]any{
		"event_id": "2", "schema_version": "1", "topic": commissionStatsTopic,
		"aggregate_type": "commission_statistics", "aggregate_id": "4", "aggregate_version": "7",
		"occurred_at": "2", "payload_json": "{}",
	})
	if result != HandleSuccess || store.statistics.StatisticsVersion != 7 || store.document.CommissionID != 0 {
		t.Fatalf("statistics envelope was not projected: %#v result=%v", store.document, result)
	}
}
func TestCommissionConsumerRejectsInvalidEnvelope(t *testing.T) {
	event := map[string]any{"event_id": "1", "schema_version": "2"}
	if _, err := parseCommissionEvent(event); err == nil {
		t.Fatal("expected invalid envelope")
	}
}

func TestCommissionConsumerDoesNotTombstoneNewerRepublication(t *testing.T) {
	store := &commissionTestStore{}
	c := &CommissionIndexConsumer{source: commissionTestSource{catalogue: commissionindex.CatalogueSnapshot{CommissionID: 4, Status: "active", CatalogueVersion: 3, Title: "Write"}, statistics: commissionindex.StatisticsSnapshot{CommissionID: 4, StatisticsVersion: 3}}, store: store, embedder: commissionTestEmbedder{}}
	result := c.Handle(context.Background(), "1-0", map[string]any{"event_id": "1", "schema_version": "1", "topic": commissionOfflineTopic, "aggregate_type": "commission", "aggregate_id": "4", "aggregate_version": "2", "occurred_at": "1", "payload_json": "{}"})
	if result != HandleSuccess || !store.document.Active {
		t.Fatalf("delayed offline event regressed republication: %#v, %v", store.document, result)
	}
}

func TestCommissionStatisticsProjectionRetries(t *testing.T) {
	for _, tc := range []struct {
		name                string
		snapshot            commissionindex.StatisticsSnapshot
		sourceErr, storeErr error
	}{
		{name: "source unavailable", sourceErr: errors.New("source unavailable")},
		{name: "stale snapshot", snapshot: commissionindex.StatisticsSnapshot{CommissionID: 4, StatisticsVersion: 6}},
		{name: "wrong identity", snapshot: commissionindex.StatisticsSnapshot{CommissionID: 5, StatisticsVersion: 7}},
		{name: "forward unavailable", snapshot: commissionindex.StatisticsSnapshot{CommissionID: 4, StatisticsVersion: 7}, storeErr: errors.New("Redis unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &commissionTestStore{err: tc.storeErr}
			c := &CommissionIndexConsumer{source: commissionTestSource{statistics: tc.snapshot, err: tc.sourceErr, forbidCatalogue: true}, store: store}
			result := c.Handle(context.Background(), "3-0", map[string]any{"event_id": "3", "schema_version": "1", "topic": commissionStatsTopic, "aggregate_type": "commission_statistics", "aggregate_id": "4", "aggregate_version": "7", "occurred_at": "3", "payload_json": "{}"})
			if result != HandleRetry {
				t.Fatalf("result = %v, want retry", result)
			}
		})
	}
}
