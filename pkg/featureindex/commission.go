package featureindex

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// CommissionIndex composes independently versioned catalogue/statistics views.
type CommissionIndex struct {
	Redis     *redis.Client
	Source    CommissionSource
	IndexName string
}

func (i CommissionIndex) View() string       { return Commission }
func (i CommissionIndex) Generation() string { return i.IndexName }
func (i CommissionIndex) Forward() Forward {
	return Forward{Redis: i.Redis, Namespace: "commission:" + i.IndexName}
}

func (i CommissionIndex) Write(ctx context.Context, d CommissionDocument) error {
	return i.WriteBatch(ctx, []CommissionDocument{d})
}
func (i CommissionIndex) WriteBatch(ctx context.Context, docs []CommissionDocument) error {
	values := make([]Mutation, 0, 2*len(docs))
	for _, d := range docs {
		stats := CommissionStatisticsSnapshot{CommissionID: d.CommissionID, SellerAgentID: d.SellerAgentID, StatisticsVersion: d.StatisticsVersion, CompletedCount: d.CompletedCount, RefundedCount: d.RefundedCount, CompletionRateBPS: d.CompletionRateBPS, AverageRatingMilli: d.AverageRatingMilli, HasRating: d.HasRating, AverageDeliveryMS: d.AverageDeliveryMS}
		d.StatisticsVersion, d.CompletedCount, d.RefundedCount, d.AverageDeliveryMS = 0, 0, 0, 0
		d.CompletionRateBPS, d.AverageRatingMilli, d.HasRating = 0, 0, false
		d.Embedding = nil
		values = append(values, Mutation{d.CommissionID, "statistics", stats.StatisticsVersion, stats}, Mutation{d.CommissionID, "catalogue", d.CatalogueVersion, d})
	}
	return i.Forward().PutBatch(ctx, values)
}

func (i CommissionIndex) WriteStatistics(ctx context.Context, s CommissionStatisticsSnapshot) error {
	return i.Forward().Put(ctx, s.CommissionID, "statistics", s.StatisticsVersion, s)
}

func (i CommissionIndex) Read(ctx context.Context, ids []int64) (map[int64]CommissionDocument, error) {
	views, err := i.Forward().readComponents(ctx, ids, "catalogue", "statistics")
	if err != nil {
		return nil, err
	}
	catalogue, err := decodeRows[CommissionDocument](views["catalogue"])
	if err != nil {
		return nil, err
	}
	statistics, err := decodeRows[CommissionStatisticsSnapshot](views["statistics"])
	if err != nil {
		return nil, err
	}
	out := make(map[int64]CommissionDocument, len(catalogue))
	for id, d := range catalogue {
		if d.CommissionID != id || d.CatalogueVersion <= 0 {
			return nil, fmt.Errorf("invalid Commission forward projection")
		}
		s, ok := statistics[id]
		if !ok {
			continue
		}
		if s.CommissionID != id || s.StatisticsVersion < 0 {
			return nil, fmt.Errorf("invalid Commission statistics projection")
		}

		d.StatisticsVersion, d.CompletedCount, d.RefundedCount = s.StatisticsVersion, s.CompletedCount, s.RefundedCount
		d.CompletionRateBPS, d.AverageRatingMilli, d.HasRating, d.AverageDeliveryMS = s.CompletionRateBPS, s.AverageRatingMilli, s.HasRating, s.AverageDeliveryMS
		out[id] = d
	}
	return out, nil
}

// LoadPage repairs active catalogue/statistics views through source RPCs.
// Offline transitions retain their versioned event/tombstone path.
func (i CommissionIndex) LoadPage(ctx context.Context, cursor int64, limit int) (nextCursor int64, resultErr error) {

	if i.Source == nil || cursor < 0 || limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("invalid Commission source page")
	}
	rows, next, err := i.Source.ListActiveIndexSnapshots(ctx, cursor, limit)
	if err != nil {
		return 0, err
	}
	ids := make([]int64, 0, len(rows))
	for _, c := range rows {
		ids = append(ids, c.CommissionID)
	}
	if len(ids) == 0 {
		return next, nil
	}
	stats, err := i.Source.BatchGetStatistics(ctx, ids)
	if err != nil {
		return 0, err
	}
	byID := map[int64]CommissionStatisticsSnapshot{}
	for _, s := range stats {
		byID[s.CommissionID] = s
	}
	docs := make([]CommissionDocument, 0, len(rows))
	for _, c := range rows {
		s, ok := byID[c.CommissionID]
		if !ok {
			return 0, fmt.Errorf("missing Commission statistics for materialization")
		}
		// RPC snapshots also serve indexing clients; omit text construction here.
		c.Title, c.CapabilityDescription, c.RequestSpecText, c.DeliverySpecText = "", "", "", ""
		c.Tags = nil
		docs = append(docs, BuildCommissionDocument(c, s, nil))
	}
	if err := i.WriteBatch(ctx, docs); err != nil {
		return 0, err
	}
	return next, nil
}
