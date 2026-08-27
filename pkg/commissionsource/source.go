// Package commissionsource adapts the Commission repository's generated RPC
// contracts to the stable projection contract owned by EigenFlux.
package commissionsource

import (
	"context"
	"eigenflux_server/kitex_gen/eigenflux/commission"
	"eigenflux_server/kitex_gen/eigenflux/commission/commissionservice"
	"eigenflux_server/kitex_gen/eigenflux/order"
	"eigenflux_server/kitex_gen/eigenflux/order/orderservice"
	"eigenflux_server/pkg/commissionindex"
	"fmt"
)

type Adapter struct {
	Commission commissionservice.Client
	Order      orderservice.Client
}

func (a Adapter) GetIndexSnapshot(ctx context.Context, id int64) (commissionindex.CatalogueSnapshot, error) {
	if id <= 0 {
		return commissionindex.CatalogueSnapshot{}, fmt.Errorf("invalid Commission ID")
	}
	resp, err := a.Commission.GetIndexSnapshot(ctx, &commission.GetIndexSnapshotReq{CommissionId: id})
	if err != nil {
		return commissionindex.CatalogueSnapshot{}, err
	}
	if resp == nil || resp.BaseResp == nil || resp.BaseResp.Code != 0 || resp.Snapshot == nil {
		return commissionindex.CatalogueSnapshot{}, fmt.Errorf("Commission snapshot failed: %s", baseMessage(resp))
	}
	return catalogue(resp.Snapshot)
}

func (a Adapter) ListActiveIndexSnapshots(ctx context.Context, cursor int64, limit int) ([]commissionindex.CatalogueSnapshot, int64, error) {
	resp, err := a.Commission.ListActiveIndexSnapshots(ctx, &commission.ListActiveIndexSnapshotsReq{Cursor: cursor, Limit: int32(limit)})
	if err != nil {
		return nil, 0, err
	}
	if resp == nil || resp.BaseResp == nil || resp.BaseResp.Code != 0 {
		return nil, 0, fmt.Errorf("list Commission snapshots failed: %s", baseListMessage(resp))
	}
	out := make([]commissionindex.CatalogueSnapshot, 0, len(resp.Snapshots))
	for _, snapshot := range resp.Snapshots {
		value, err := catalogue(snapshot)
		if err != nil {
			return nil, 0, err
		}
		if value.Status != "active" {
			return nil, 0, fmt.Errorf("source returned incomplete active snapshot for %d", value.CommissionID)
		}
		out = append(out, value)
	}
	return out, resp.NextCursor, nil
}

func (a Adapter) GetStatistics(ctx context.Context, id int64) (commissionindex.StatisticsSnapshot, error) {
	resp, err := a.Order.GetCommissionStatistics(ctx, &order.GetCommissionStatisticsReq{CommissionId: id})
	if err != nil {
		return commissionindex.StatisticsSnapshot{}, err
	}
	if resp == nil || resp.BaseResp == nil || resp.BaseResp.Code != 0 || resp.Statistics == nil {
		return commissionindex.StatisticsSnapshot{}, fmt.Errorf("get Commission statistics failed")
	}
	return statistics(resp.Statistics), nil
}

func (a Adapter) BatchGetStatistics(ctx context.Context, ids []int64) ([]commissionindex.StatisticsSnapshot, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	resp, err := a.Order.BatchGetCommissionStatistics(ctx, &order.BatchGetCommissionStatisticsReq{CommissionIds: ids})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.BaseResp == nil || resp.BaseResp.Code != 0 {
		return nil, fmt.Errorf("batch Commission statistics failed")
	}
	out := make([]commissionindex.StatisticsSnapshot, 0, len(resp.Statistics))
	for _, value := range resp.Statistics {
		if value == nil {
			continue
		}
		out = append(out, statistics(value))
	}
	return out, nil
}

func catalogue(value *commission.CommissionIndexSnapshot) (commissionindex.CatalogueSnapshot, error) {
	if value == nil || value.Definition == nil || value.Revision == nil || value.Revision.Content == nil {
		return commissionindex.CatalogueSnapshot{}, fmt.Errorf("incomplete Commission snapshot")
	}
	d, c := value.Definition, value.Revision.Content
	if d.CommissionId <= 0 || d.Version <= 0 {
		return commissionindex.CatalogueSnapshot{}, fmt.Errorf("invalid Commission snapshot")
	}
	return commissionindex.CatalogueSnapshot{CommissionID: d.CommissionId, SellerAgentID: d.SellerAgentId, Status: d.Status, CatalogueVersion: d.Version, Title: c.Title, CapabilityDescription: c.CapabilityDescription, RequestSpecText: c.RequestSpecText, DeliverySpecText: c.DeliverySpecText, Tags: c.Tags, PriceFen: c.PriceFen, Currency: c.Currency, PromisedDeliveryMS: c.PromisedDeliveryMs, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}, nil
}
func statistics(value *order.CommissionStatistics) commissionindex.StatisticsSnapshot {
	return commissionindex.StatisticsSnapshot{CommissionID: value.CommissionId, SellerAgentID: value.SellerAgentId, CompletedCount: value.CompletedCount, RefundedCount: value.RefundedCount, CompletionRateBPS: value.CompletionRateBps, AverageRatingMilli: value.AverageRatingMilli, HasRating: value.HasRating, AverageDeliveryMS: value.AverageDeliveryMs, StatisticsVersion: value.StatisticsVersion}
}
func baseMessage(resp *commission.GetIndexSnapshotResp) string {
	if resp == nil || resp.BaseResp == nil {
		return "empty response"
	}
	return resp.BaseResp.Msg
}
func baseListMessage(resp *commission.ListActiveIndexSnapshotsResp) string {
	if resp == nil || resp.BaseResp == nil {
		return "empty response"
	}
	return resp.BaseResp.Msg
}
