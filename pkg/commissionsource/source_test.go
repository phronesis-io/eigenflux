package commissionsource

import (
	"context"
	"errors"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/commission"
	"eigenflux_server/kitex_gen/eigenflux/commission/commissionservice"
	"eigenflux_server/kitex_gen/eigenflux/order"
	"eigenflux_server/kitex_gen/eigenflux/order/orderservice"

	"github.com/cloudwego/kitex/client/callopt"
)

type commissionClientFake struct {
	get  func(context.Context, *commission.GetIndexSnapshotReq) (*commission.GetIndexSnapshotResp, error)
	list func(context.Context, *commission.ListActiveIndexSnapshotsReq) (*commission.ListActiveIndexSnapshotsResp, error)
}

func (f commissionClientFake) GetIndexSnapshot(ctx context.Context, req *commission.GetIndexSnapshotReq, _ ...callopt.Option) (*commission.GetIndexSnapshotResp, error) {
	if f.get == nil {
		return nil, errors.New("unexpected GetIndexSnapshot")
	}
	return f.get(ctx, req)
}

func (f commissionClientFake) ListActiveIndexSnapshots(ctx context.Context, req *commission.ListActiveIndexSnapshotsReq, _ ...callopt.Option) (*commission.ListActiveIndexSnapshotsResp, error) {
	if f.list == nil {
		return nil, errors.New("unexpected ListActiveIndexSnapshots")
	}
	return f.list(ctx, req)
}

type orderClientFake struct {
	get   func(context.Context, *order.GetCommissionStatisticsReq) (*order.GetCommissionStatisticsResp, error)
	batch func(context.Context, *order.BatchGetCommissionStatisticsReq) (*order.BatchGetCommissionStatisticsResp, error)
}

func (f orderClientFake) GetCommissionStatistics(ctx context.Context, req *order.GetCommissionStatisticsReq, _ ...callopt.Option) (*order.GetCommissionStatisticsResp, error) {
	if f.get == nil {
		return nil, errors.New("unexpected GetCommissionStatistics")
	}
	return f.get(ctx, req)
}

func (f orderClientFake) BatchGetCommissionStatistics(ctx context.Context, req *order.BatchGetCommissionStatisticsReq, _ ...callopt.Option) (*order.BatchGetCommissionStatisticsResp, error) {
	if f.batch == nil {
		return nil, errors.New("unexpected BatchGetCommissionStatistics")
	}
	return f.batch(ctx, req)
}

var _ commissionservice.Client = commissionClientFake{}
var _ orderservice.Client = orderClientFake{}

func successfulCatalogueSnapshot(id int64, status string) *commission.CommissionIndexSnapshot {
	return &commission.CommissionIndexSnapshot{
		Definition: &commission.CommissionDefinition{
			CommissionId: id, SellerAgentId: 9223372036854775001, Status: status,
			PublicRevision: 5, Version: 12, CreatedAt: 1001, UpdatedAt: 1002,
		},
		Revision: &commission.CommissionRevision{
			CommissionId: id, SellerAgentId: 9223372036854775001, Revision: 5,
			Content: &commission.CommissionInput{
				Title: "private catalogue title", CapabilityDescription: "capability",
				RequestSpecText: "request", DeliverySpecText: "delivery", Tags: []string{"go", "api"},
				PriceFen: 12345, Currency: "CNY", PromisedDeliveryMs: 9876,
			},
		},
	}
}

func TestAdapterMapsCatalogueAndPreservesInt64ID(t *testing.T) {
	const id int64 = 9223372036854775000
	adapter := Adapter{Commission: commissionClientFake{get: func(_ context.Context, req *commission.GetIndexSnapshotReq) (*commission.GetIndexSnapshotResp, error) {
		if req.CommissionId != id {
			t.Fatalf("commission ID=%d", req.CommissionId)
		}
		return &commission.GetIndexSnapshotResp{Snapshot: successfulCatalogueSnapshot(id, "active"), BaseResp: &base.BaseResp{}}, nil
	}}}

	got, err := adapter.GetIndexSnapshot(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommissionID != id || got.SellerAgentID != 9223372036854775001 || got.Status != "active" ||
		got.CatalogueVersion != 12 || got.Title != "private catalogue title" || got.CapabilityDescription != "capability" ||
		got.RequestSpecText != "request" || got.DeliverySpecText != "delivery" || got.PriceFen != 12345 ||
		got.Currency != "CNY" || got.PromisedDeliveryMS != 9876 || got.CreatedAt != 1001 || got.UpdatedAt != 1002 || len(got.Tags) != 2 {
		t.Fatalf("unexpected catalogue mapping: %#v", got)
	}
}

func TestAdapterMapsDefinitionOnlyOfflinePointRead(t *testing.T) {
	const id int64 = 9223372036854775000
	adapter := Adapter{Commission: commissionClientFake{get: func(context.Context, *commission.GetIndexSnapshotReq) (*commission.GetIndexSnapshotResp, error) {
		return &commission.GetIndexSnapshotResp{
			Snapshot: &commission.CommissionIndexSnapshot{
				Definition: &commission.CommissionDefinition{
					CommissionId: id, SellerAgentId: 9223372036854775001, Status: "offline",
					PublicRevision: 5, Version: 13, CreatedAt: 1001, UpdatedAt: 2002,
				},
				Revision: &commission.CommissionRevision{},
			},
			BaseResp: &base.BaseResp{},
		}, nil
	}}}

	got, err := adapter.GetIndexSnapshot(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommissionID != id || got.SellerAgentID != 9223372036854775001 || got.Status != "offline" ||
		got.CatalogueVersion != 13 || got.CreatedAt != 1001 || got.UpdatedAt != 2002 {
		t.Fatalf("unexpected offline snapshot: %#v", got)
	}
	if got.Title != "" || got.CapabilityDescription != "" || got.RequestSpecText != "" || got.DeliverySpecText != "" || len(got.Tags) != 0 {
		t.Fatalf("offline point read exposed revision content: %#v", got)
	}
}

func TestAdapterRejectsIncompleteCatalogueResponses(t *testing.T) {
	id := int64(42)
	tests := map[string]*commission.GetIndexSnapshotResp{
		"nil base response":     {Snapshot: successfulCatalogueSnapshot(id, "active")},
		"nil response":          nil,
		"nonzero base response": {Snapshot: successfulCatalogueSnapshot(id, "active"), BaseResp: &base.BaseResp{Code: 1, Msg: "private source text"}},
		"nil snapshot":          {BaseResp: &base.BaseResp{}},
		"nil definition":        {Snapshot: &commission.CommissionIndexSnapshot{Revision: &commission.CommissionRevision{Content: &commission.CommissionInput{}}}, BaseResp: &base.BaseResp{}},
		"nil revision":          {Snapshot: &commission.CommissionIndexSnapshot{Definition: &commission.CommissionDefinition{CommissionId: id, Version: 1}}, BaseResp: &base.BaseResp{}},
		"nil content":           {Snapshot: &commission.CommissionIndexSnapshot{Definition: &commission.CommissionDefinition{CommissionId: id, Version: 1}, Revision: &commission.CommissionRevision{}}, BaseResp: &base.BaseResp{}},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			adapter := Adapter{Commission: commissionClientFake{get: func(context.Context, *commission.GetIndexSnapshotReq) (*commission.GetIndexSnapshotResp, error) {
				return response, nil
			}}}
			if _, err := adapter.GetIndexSnapshot(context.Background(), id); err == nil {
				t.Fatal("incomplete response accepted")
			}
		})
	}
}

func TestAdapterRejectsMismatchedCatalogueIdentity(t *testing.T) {
	adapter := Adapter{Commission: commissionClientFake{get: func(context.Context, *commission.GetIndexSnapshotReq) (*commission.GetIndexSnapshotResp, error) {
		return &commission.GetIndexSnapshotResp{Snapshot: successfulCatalogueSnapshot(43, "active"), BaseResp: &base.BaseResp{}}, nil
	}}}
	if _, err := adapter.GetIndexSnapshot(context.Background(), 42); err == nil {
		t.Fatal("mismatched catalogue identity accepted")
	}
}

func TestAdapterListActiveRejectsIncompleteAndOfflineSnapshots(t *testing.T) {
	for name, snapshot := range map[string]*commission.CommissionIndexSnapshot{
		"nil":        nil,
		"incomplete": {Definition: &commission.CommissionDefinition{CommissionId: 42, Version: 1}},
		"offline":    successfulCatalogueSnapshot(42, "offline"),
	} {
		t.Run(name, func(t *testing.T) {
			adapter := Adapter{Commission: commissionClientFake{list: func(_ context.Context, req *commission.ListActiveIndexSnapshotsReq) (*commission.ListActiveIndexSnapshotsResp, error) {
				if req.Cursor != 9223372036854775000 || req.Limit != 25 {
					t.Fatalf("request=%#v", req)
				}
				return &commission.ListActiveIndexSnapshotsResp{Snapshots: []*commission.CommissionIndexSnapshot{snapshot}, BaseResp: &base.BaseResp{}}, nil
			}}}
			if _, _, err := adapter.ListActiveIndexSnapshots(context.Background(), 9223372036854775000, 25); err == nil {
				t.Fatal("invalid active snapshot accepted")
			}
		})
	}
}

func TestAdapterMapsStatisticsAndPreservesInt64ID(t *testing.T) {
	const id int64 = 9223372036854775000
	statistics := &order.CommissionStatistics{
		CommissionId: id, SellerAgentId: 9223372036854775001, CompletedCount: 9, RefundedCount: 2,
		CompletionRateBps: 8181, AverageRatingMilli: 4555, HasRating: true,
		AverageDeliveryMs: 123456, StatisticsVersion: 77,
	}
	adapter := Adapter{Order: orderClientFake{get: func(_ context.Context, req *order.GetCommissionStatisticsReq) (*order.GetCommissionStatisticsResp, error) {
		if req.CommissionId != id {
			t.Fatalf("commission ID=%d", req.CommissionId)
		}
		return &order.GetCommissionStatisticsResp{Statistics: statistics, BaseResp: &base.BaseResp{}}, nil
	}}}
	got, err := adapter.GetStatistics(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommissionID != id || got.SellerAgentID != 9223372036854775001 || got.CompletedCount != 9 ||
		got.RefundedCount != 2 || got.CompletionRateBPS != 8181 || got.AverageRatingMilli != 4555 ||
		!got.HasRating || got.AverageDeliveryMS != 123456 || got.StatisticsVersion != 77 {
		t.Fatalf("unexpected statistics mapping: %#v", got)
	}
}

func TestAdapterRejectsIncompleteStatisticsResponses(t *testing.T) {
	id := int64(42)
	for name, response := range map[string]*order.GetCommissionStatisticsResp{
		"nil response":          nil,
		"nil base response":     {Statistics: &order.CommissionStatistics{CommissionId: id}},
		"nonzero base response": {Statistics: &order.CommissionStatistics{CommissionId: id}, BaseResp: &base.BaseResp{Code: 1, Msg: "private profile"}},
		"nil statistics":        {BaseResp: &base.BaseResp{}},
		"zero statistics ID":    {Statistics: &order.CommissionStatistics{}, BaseResp: &base.BaseResp{}},
	} {
		t.Run(name, func(t *testing.T) {
			adapter := Adapter{Order: orderClientFake{get: func(context.Context, *order.GetCommissionStatisticsReq) (*order.GetCommissionStatisticsResp, error) {
				return response, nil
			}}}
			if _, err := adapter.GetStatistics(context.Background(), id); err == nil {
				t.Fatal("incomplete statistics response accepted")
			}
		})
	}
}

func TestAdapterBatchStatisticsSkipsNilEntries(t *testing.T) {
	ids := []int64{9223372036854775000, 9223372036854775001}
	adapter := Adapter{Order: orderClientFake{batch: func(_ context.Context, req *order.BatchGetCommissionStatisticsReq) (*order.BatchGetCommissionStatisticsResp, error) {
		if len(req.CommissionIds) != 2 || req.CommissionIds[0] != ids[0] || req.CommissionIds[1] != ids[1] {
			t.Fatalf("IDs=%v", req.CommissionIds)
		}
		return &order.BatchGetCommissionStatisticsResp{
			Statistics: []*order.CommissionStatistics{nil, {CommissionId: ids[1], StatisticsVersion: 5}},
			BaseResp:   &base.BaseResp{},
		}, nil
	}}}
	got, err := adapter.BatchGetStatistics(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CommissionID != ids[1] || got[0].StatisticsVersion != 5 {
		t.Fatalf("statistics=%#v", got)
	}
}

func TestAdapterReadyProbesBothSourceClients(t *testing.T) {
	commissionCalled := false
	orderCalled := false
	adapter := Adapter{
		Commission: commissionClientFake{list: func(context.Context, *commission.ListActiveIndexSnapshotsReq) (*commission.ListActiveIndexSnapshotsResp, error) {
			commissionCalled = true
			return &commission.ListActiveIndexSnapshotsResp{BaseResp: &base.BaseResp{}}, nil
		}},
		Order: orderClientFake{get: func(_ context.Context, req *order.GetCommissionStatisticsReq) (*order.GetCommissionStatisticsResp, error) {
			orderCalled = true
			if req.CommissionId != 1 {
				t.Fatalf("probe ID=%d", req.CommissionId)
			}
			return &order.GetCommissionStatisticsResp{Statistics: &order.CommissionStatistics{CommissionId: 1}, BaseResp: &base.BaseResp{}}, nil
		}},
	}
	if err := adapter.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !commissionCalled || !orderCalled {
		t.Fatalf("commissionCalled=%v orderCalled=%v", commissionCalled, orderCalled)
	}
}

func TestAdapterReadyFailsWhenEitherSourceIsUnavailable(t *testing.T) {
	tests := map[string]struct{ commission, order error }{
		"commission": {commission: errors.New("private Commission error")},
		"order":      {order: errors.New("private Order error")},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			adapter := Adapter{
				Commission: commissionClientFake{list: func(context.Context, *commission.ListActiveIndexSnapshotsReq) (*commission.ListActiveIndexSnapshotsResp, error) {
					if tc.commission != nil {
						return nil, tc.commission
					}
					return &commission.ListActiveIndexSnapshotsResp{BaseResp: &base.BaseResp{}}, nil
				}},
				Order: orderClientFake{get: func(context.Context, *order.GetCommissionStatisticsReq) (*order.GetCommissionStatisticsResp, error) {
					if tc.order != nil {
						return nil, tc.order
					}
					return &order.GetCommissionStatisticsResp{Statistics: &order.CommissionStatistics{CommissionId: 1}, BaseResp: &base.BaseResp{}}, nil
				}},
			}
			if err := adapter.Ready(context.Background()); err == nil {
				t.Fatal("unavailable source reported ready")
			}
		})
	}
}
