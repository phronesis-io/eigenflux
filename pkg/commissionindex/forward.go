package commissionindex

import (
	"context"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

func Forward(rdb *redis.Client, index string) searchindex.Forward {
	return searchindex.Forward{Redis: rdb, Namespace: "commission:" + index}
}

func WriteForward(ctx context.Context, rdb *redis.Client, index string, d Document) error {
	stats := StatisticsSnapshot{CommissionID: d.CommissionID, SellerAgentID: d.SellerAgentID, StatisticsVersion: d.StatisticsVersion,
		CompletedCount: d.CompletedCount, RefundedCount: d.RefundedCount, CompletionRateBPS: d.CompletionRateBPS,
		AverageRatingMilli: d.AverageRatingMilli, HasRating: d.HasRating, AverageDeliveryMS: d.AverageDeliveryMS}
	// Statistics and catalogue revisions advance independently.
	if err := WriteStatistics(ctx, rdb, index, stats); err != nil {
		return err
	}
	d.StatisticsVersion, d.CompletedCount, d.RefundedCount, d.AverageDeliveryMS = 0, 0, 0, 0
	d.CompletionRateBPS, d.AverageRatingMilli, d.HasRating = 0, 0, false
	return Forward(rdb, index).Put(ctx, d.CommissionID, "catalogue", d.CatalogueVersion, d)
}

func WriteStatistics(ctx context.Context, rdb *redis.Client, index string, s StatisticsSnapshot) error {
	return Forward(rdb, index).Put(ctx, s.CommissionID, "statistics", s.StatisticsVersion, s)
}

func ReadForward(ctx context.Context, rdb *redis.Client, index string, ids []int64) (map[int64]Document, error) {
	f := Forward(rdb, index)
	catalogue, err := f.Get(ctx, ids, "catalogue")
	if err != nil {
		return nil, err
	}
	statistics, err := f.Get(ctx, ids, "statistics")
	if err != nil {
		return nil, err
	}
	out := map[int64]Document{}
	for id, raw := range catalogue {
		var d Document
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		if d.CommissionID != id || d.CatalogueVersion <= 0 {
			return nil, fmt.Errorf("invalid Commission forward projection")
		}
		rawStats, ok := statistics[id]
		if !ok {
			continue
		}
		var s StatisticsSnapshot
		if err := json.Unmarshal(rawStats, &s); err != nil {
			return nil, err
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

func (d Document) SearchFields() map[string]any {
	return map[string]any{"commission_id": d.CommissionID, "seller_agent_id": d.SellerAgentID,
		"active": d.Active, "catalogue_version": d.CatalogueVersion, "title": d.Title,
		"capability_description": d.CapabilityDescription, "request_spec_text": d.RequestSpecText, "delivery_spec_text": d.DeliverySpecText,
		"search_text": d.SearchText, "price_fen": d.PriceFen, "currency": d.Currency,
		"promised_delivery_ms": d.PromisedDeliveryMS, "retrieval_slots": d.RetrievalSlots, "embedding": d.Embedding}
}
