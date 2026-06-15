package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/trade"
	"eigenflux_server/pkg/chief"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/schemaval"
	"eigenflux_server/rpc/trade/dal"

	"gorm.io/gorm"
)

type idGenerator interface {
	NextID() (int64, error)
}

type TradeServiceImpl struct {
	serviceIDGen  idGenerator
	orderIDGen    idGenerator
	eventIDGen    idGenerator
	receiptIDGen  idGenerator
	outboxIDGen   idGenerator
	chiefClient   *chief.Client
	chiefLookback int
	maxActive     int
}

func ok() *base.BaseResp            { return &base.BaseResp{Code: 0, Msg: "success"} }
func fail(msg string) *base.BaseResp { return &base.BaseResp{Code: 500, Msg: msg} }
func badReq(msg string) *base.BaseResp { return &base.BaseResp{Code: 400, Msg: msg} }

var allowedAssets = map[string]bool{"USDC": true}

var errAlreadyAtTarget = errors.New("order already at target status")
var errOrderNotFound = errors.New("order not found")

// --- Service Declaration ---

func (s *TradeServiceImpl) PublishService(_ context.Context, req *trade.PublishServiceReq) (*trade.PublishServiceResp, error) {
	if req.Asset == "" {
		req.Asset = "USDC"
	}
	if !allowedAssets[req.Asset] {
		return &trade.PublishServiceResp{BaseResp: badReq("unsupported asset: " + req.Asset)}, nil
	}

	id, err := s.serviceIDGen.NextID()
	if err != nil {
		return &trade.PublishServiceResp{BaseResp: fail("id gen: " + err.Error())}, nil
	}

	svc := &dal.TradingService{
		ServiceID:          id,
		SellerAgentID:      req.SellerAgentId,
		Title:              req.Title,
		CapabilityDesc:     req.CapabilityDesc,
		CallSpecText:       req.CallSpecText,
		PriceText:          req.PriceText,
		AmountAtomic:       req.AmountAtomic,
		Asset:              req.Asset,
		DeliveryDeadlineMs: req.DeliveryDeadlineMs,
	}
	if req.CallSpecSchema != "" {
		schema := req.CallSpecSchema
		svc.CallSpecSchema = &schema
	}
	if err := dal.CreateService(db.DB, svc); err != nil {
		return &trade.PublishServiceResp{BaseResp: fail("create service: " + err.Error())}, nil
	}

	_, _ = mq.Publish(context.Background(), "stream:trade:service", map[string]interface{}{
		"service_id": fmt.Sprintf("%d", id),
		"action":     "publish",
	})

	return &trade.PublishServiceResp{ServiceId: id, BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) UpdateService(_ context.Context, req *trade.UpdateServiceReq) (*trade.UpdateServiceResp, error) {
	if req.Asset != "" && !allowedAssets[req.Asset] {
		return &trade.UpdateServiceResp{BaseResp: badReq("unsupported asset: " + req.Asset)}, nil
	}

	// The Thrift RPC IDL declares these fields as plain primitives, so "unset"
	// arrives at the handler as the zero value. The API gateway already filters
	// out fields the caller did not send (api.thrift uses `optional` pointers
	// there) — honor that contract by skipping zero-valued fields here. This
	// also prevents an empty `call_spec_schema = ""` from being written into a
	// jsonb column, which postgres rejects as invalid JSON.
	updates := map[string]interface{}{}
	if req.Title != "" {
		updates["title"] = req.Title
	}
	if req.CapabilityDesc != "" {
		updates["capability_desc"] = req.CapabilityDesc
	}
	if req.CallSpecText != "" {
		updates["call_spec_text"] = req.CallSpecText
	}
	if req.CallSpecSchema != "" {
		updates["call_spec_schema"] = req.CallSpecSchema
	}
	if req.PriceText != "" {
		updates["price_text"] = req.PriceText
	}
	if req.AmountAtomic != 0 {
		updates["amount_atomic"] = req.AmountAtomic
	}
	if req.Asset != "" {
		updates["asset"] = req.Asset
	}
	if req.DeliveryDeadlineMs != 0 {
		updates["delivery_deadline_ms"] = req.DeliveryDeadlineMs
	}
	if len(updates) == 0 {
		return &trade.UpdateServiceResp{BaseResp: ok()}, nil
	}
	if err := dal.UpdateService(db.DB, req.ServiceId, req.SellerAgentId, updates); err != nil {
		return &trade.UpdateServiceResp{BaseResp: fail("update: " + err.Error())}, nil
	}

	_, _ = mq.Publish(context.Background(), "stream:trade:service", map[string]interface{}{
		"service_id": fmt.Sprintf("%d", req.ServiceId),
		"action":     "update",
	})

	return &trade.UpdateServiceResp{BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) OfflineService(_ context.Context, req *trade.OfflineServiceReq) (*trade.OfflineServiceResp, error) {
	if err := dal.OfflineService(db.DB, req.ServiceId, req.SellerAgentId); err != nil {
		return &trade.OfflineServiceResp{BaseResp: fail("offline: " + err.Error())}, nil
	}
	return &trade.OfflineServiceResp{BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) GetMyServices(_ context.Context, req *trade.GetMyServicesReq) (*trade.GetMyServicesResp, error) {
	limit := int(req.Limit)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var cursor int64
	if req.Cursor != "" {
		fmt.Sscanf(req.Cursor, "%d", &cursor)
	}

	services, err := dal.ListServicesBySeller(db.DB, req.SellerAgentId, limit+1, cursor)
	if err != nil {
		return &trade.GetMyServicesResp{BaseResp: fail("list: " + err.Error())}, nil
	}

	var nextCursor string
	if len(services) > limit {
		nextCursor = fmt.Sprintf("%d", services[limit-1].ServiceID)
		services = services[:limit]
	}

	ids := make([]int64, len(services))
	for i, svc := range services {
		ids[i] = svc.ServiceID
	}
	stats, err := dal.BatchGetServiceStats(db.DB, ids)
	if err != nil {
		return &trade.GetMyServicesResp{BaseResp: fail("stats: " + err.Error())}, nil
	}

	items := make([]*trade.TradingService, len(services))
	for i, svc := range services {
		items[i] = svcToThrift(svc, stats[svc.ServiceID])
	}

	return &trade.GetMyServicesResp{Services: items, NextCursor: nextCursor, BaseResp: ok()}, nil
}

// --- Orders ---

func (s *TradeServiceImpl) CreateOrder(ctx context.Context, req *trade.CreateOrderReq) (*trade.CreateOrderResp, error) {
	if req.IdempotencyKey != "" {
		existing, err := dal.FindOrderByIdempotencyKey(db.DB, req.BuyerAgentId, req.IdempotencyKey)
		if err == nil {
			return &trade.CreateOrderResp{OrderId: existing.OrderID, BaseResp: ok()}, nil
		}
	}

	var orderID int64
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", req.BuyerAgentId).Error; err != nil {
			return fmt.Errorf("advisory lock: %w", err)
		}

		if req.IdempotencyKey != "" {
			existing, err := dal.FindOrderByIdempotencyKey(tx, req.BuyerAgentId, req.IdempotencyKey)
			if err == nil {
				orderID = existing.OrderID
				return errAlreadyAtTarget
			}
		}

		gate, err := checkBuyerGate(tx, req.BuyerAgentId, s.maxActive)
		if err != nil {
			return fmt.Errorf("gate check: %w", err)
		}
		if !gate.CanCreate {
			return errors.New("buyer gate blocked")
		}

		svc, err := dal.GetService(tx, req.ServiceId)
		if err != nil {
			return fmt.Errorf("service not found: %w", err)
		}
		if svc.Status != dal.ServiceStatusActive {
			return errors.New("service not active")
		}
		if svc.SellerAgentID == req.BuyerAgentId {
			return errors.New("cannot buy own service")
		}

		if svc.CallSpecSchema != nil && *svc.CallSpecSchema != "" && req.BuyerInput != "" {
			if err := schemaval.Validate(*svc.CallSpecSchema, req.BuyerInput); err != nil {
				return fmt.Errorf("buyer_input validation: %w", err)
			}
		}

		newID, err := s.orderIDGen.NextID()
		if err != nil {
			return fmt.Errorf("id gen: %w", err)
		}

		order := &dal.TradeOrder{
			OrderID:                  newID,
			ServiceID:                req.ServiceId,
			BuyerAgentID:             req.BuyerAgentId,
			SellerAgentID:            svc.SellerAgentID,
			Status:                   dal.OrderStatusCreated,
			FrozenTitle:              svc.Title,
			FrozenCallSpecText:       svc.CallSpecText,
			FrozenCallSpecSchema:     svc.CallSpecSchema,
			FrozenAmountAtomic:       svc.AmountAtomic,
			FrozenAsset:              svc.Asset,
			FrozenDeliveryDeadlineMs: svc.DeliveryDeadlineMs,
			BuyerInput:               req.BuyerInput,
			IdempotencyKey:           req.IdempotencyKey,
		}
		if err := dal.CreateOrder(tx, order); err != nil {
			return fmt.Errorf("create order: %w", err)
		}
		orderID = newID
		return s.appendEvent(tx, newID, dal.EventTypeCreated, req.BuyerAgentId, "")
	})

	switch {
	case errors.Is(err, errAlreadyAtTarget):
		return &trade.CreateOrderResp{OrderId: orderID, BaseResp: ok()}, nil
	case err != nil:
		return &trade.CreateOrderResp{BaseResp: mapCreateOrderErr(err)}, nil
	}
	return &trade.CreateOrderResp{OrderId: orderID, BaseResp: ok()}, nil
}


func (s *TradeServiceImpl) DeliverOrder(_ context.Context, req *trade.DeliverOrderReq) (*trade.DeliverOrderResp, error) {
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		order, err := dal.GetOrder(tx, req.OrderId)
		if err != nil {
			return errOrderNotFound
		}
		if order.SellerAgentID != req.SellerAgentId {
			return errors.New("not seller")
		}
		if order.Status == dal.OrderStatusDelivered {
			return errAlreadyAtTarget
		}
		if err := validateTransition(order.Status, dal.OrderStatusDelivered); err != nil {
			return err
		}

		now := time.Now().UnixMilli()
		if err := dal.TransitionOrderStatus(tx, req.OrderId, order.Status, dal.OrderStatusDelivered, map[string]interface{}{
			"delivery_payload": req.DeliveryPayload,
			"delivered_at":     now,
		}); err != nil {
			if errors.Is(err, dal.ErrTransitionConflict) {
				cur, gerr := dal.GetOrder(tx, req.OrderId)
				if gerr == nil && cur.Status == dal.OrderStatusDelivered {
					return errAlreadyAtTarget
				}
			}
			return err
		}
		return s.appendEvent(tx, req.OrderId, dal.EventTypeDelivered, req.SellerAgentId, "")
	})
	return &trade.DeliverOrderResp{BaseResp: mapHandlerErr(err)}, nil
}

func (s *TradeServiceImpl) ReleaseOrder(ctx context.Context, req *trade.ReleaseOrderReq) (*trade.ReleaseOrderResp, error) {
	if req.TransferId == "" {
		return &trade.ReleaseOrderResp{BaseResp: badReq("transfer_id required")}, nil
	}
	var verifyResult *chief.VerifyResult
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		order, err := dal.GetOrder(tx, req.OrderId)
		if err != nil {
			return errOrderNotFound
		}
		if order.BuyerAgentID != req.BuyerAgentId {
			return errors.New("not buyer")
		}
		if order.Status == dal.OrderStatusReleased {
			return errAlreadyAtTarget
		}
		if err := validateTransition(order.Status, dal.OrderStatusReleased); err != nil {
			return err
		}

		vr, err := s.chiefClient.VerifyAgentTransfer(ctx, chief.VerifyReq{
			TransferID:      req.TransferId,
			FromAgentID:     order.BuyerAgentID,
			ToAgentID:       order.SellerAgentID,
			Asset:           order.FrozenAsset,
			MinAmountAtomic: order.FrozenAmountAtomic,
		}, s.chiefLookback)
		if err != nil {
			return fmt.Errorf("chief verify: %w", err)
		}
		if !vr.Matched {
			return fmt.Errorf("transfer verification failed: %s", string(vr.Reason))
		}
		verifyResult = &vr

		now := time.Now().UnixMilli()
		if err := dal.TransitionOrderStatus(tx, req.OrderId, order.Status, dal.OrderStatusReleased, map[string]interface{}{
			"transfer_id":    req.TransferId,
			"transfer_state": "released",
			"paid_at":        now,
			"released_at":    now,
			"closed_at":      now,
		}); err != nil {
			if errors.Is(err, dal.ErrTransitionConflict) {
				cur, gerr := dal.GetOrder(tx, req.OrderId)
				if gerr == nil && cur.Status == dal.OrderStatusReleased {
					return errAlreadyAtTarget
				}
			}
			return err
		}
		if err := s.appendEvent(tx, req.OrderId, dal.EventTypeReleased, req.BuyerAgentId, ""); err != nil {
			return err
		}
		if err := s.saveTransferReceipt(tx, req.OrderId, req.TransferId, "released", verifyResult.Entry); err != nil {
			return err
		}
		return s.enqueueOrderEvent(tx, req.OrderId, order.ServiceID, dal.EventTypeReleased)
	})
	return &trade.ReleaseOrderResp{BaseResp: mapHandlerErr(err)}, nil
}

func (s *TradeServiceImpl) RefundOrder(_ context.Context, req *trade.RefundOrderReq) (*trade.RefundOrderResp, error) {
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		order, err := dal.GetOrder(tx, req.OrderId)
		if err != nil {
			return errOrderNotFound
		}
		if order.Status == dal.OrderStatusRefunded {
			return errAlreadyAtTarget
		}
		if err := validateTransition(order.Status, dal.OrderStatusRefunded); err != nil {
			return err
		}

		now := time.Now().UnixMilli()
		if err := dal.TransitionOrderStatus(tx, req.OrderId, order.Status, dal.OrderStatusRefunded, map[string]interface{}{
			"transfer_state": "refunded",
			"refunded_at":    now,
			"closed_at":      now,
		}); err != nil {
			if errors.Is(err, dal.ErrTransitionConflict) {
				cur, gerr := dal.GetOrder(tx, req.OrderId)
				if gerr == nil && cur.Status == dal.OrderStatusRefunded {
					return errAlreadyAtTarget
				}
			}
			return err
		}
		if err := s.appendEvent(tx, req.OrderId, dal.EventTypeRefunded, req.ActorAgentId, ""); err != nil {
			return err
		}
		if err := s.saveTransferReceipt(tx, req.OrderId, order.TransferID, "refunded", nil); err != nil {
			return err
		}
		return s.enqueueOrderEvent(tx, req.OrderId, order.ServiceID, dal.EventTypeRefunded)
	})
	return &trade.RefundOrderResp{BaseResp: mapHandlerErr(err)}, nil
}

func (s *TradeServiceImpl) GetOrder(_ context.Context, req *trade.GetOrderReq) (*trade.GetOrderResp, error) {
	order, err := dal.GetOrder(db.DB, req.OrderId)
	if err != nil {
		return &trade.GetOrderResp{BaseResp: fail("order not found")}, nil
	}
	if order.BuyerAgentID != req.AgentId && order.SellerAgentID != req.AgentId {
		return &trade.GetOrderResp{BaseResp: badReq("not authorized")}, nil
	}

	events, _ := dal.ListOrderEvents(db.DB, req.OrderId)
	return &trade.GetOrderResp{
		Order:    orderToThrift(order),
		Events:   eventsToThrift(events),
		BaseResp: ok(),
	}, nil
}

func (s *TradeServiceImpl) ListOrders(_ context.Context, req *trade.ListOrdersReq) (*trade.ListOrdersResp, error) {
	limit := int(req.Limit)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var cursor int64
	if req.Cursor != "" {
		fmt.Sscanf(req.Cursor, "%d", &cursor)
	}

	orders, err := dal.ListOrdersByAgent(db.DB, req.AgentId, req.Role, req.StatusFilter, limit+1, cursor)
	if err != nil {
		return &trade.ListOrdersResp{BaseResp: fail("list: " + err.Error())}, nil
	}

	var nextCursor string
	if len(orders) > limit {
		nextCursor = fmt.Sprintf("%d", orders[limit-1].OrderID)
		orders = orders[:limit]
	}

	items := make([]*trade.TradeOrder, len(orders))
	for i, o := range orders {
		items[i] = orderToThrift(o)
	}

	return &trade.ListOrdersResp{Orders: items, NextCursor: nextCursor, BaseResp: ok()}, nil
}

func (s *TradeServiceImpl) GetGateStatus(_ context.Context, req *trade.GetGateStatusReq) (*trade.GetGateStatusResp, error) {
	gate, err := checkBuyerGate(db.DB, req.BuyerAgentId, s.maxActive)
	if err != nil {
		return &trade.GetGateStatusResp{BaseResp: fail("gate: " + err.Error())}, nil
	}
	return &trade.GetGateStatusResp{
		CanCreateOrder:    gate.CanCreate,
		ActiveOrderCount:  gate.ActiveCount,
		MaxActiveOrders:   gate.MaxActive,
		HasPendingRelease: gate.HasPendingRelease,
		BaseResp:          ok(),
	}, nil
}

// --- Helpers ---

func (s *TradeServiceImpl) appendEvent(tx *gorm.DB, orderID int64, eventType int16, actorID int64, payload string) error {
	eventID, err := s.eventIDGen.NextID()
	if err != nil {
		return fmt.Errorf("event id gen: %w", err)
	}
	event := &dal.TradeOrderEvent{
		EventID:      eventID,
		OrderID:      orderID,
		EventType:    eventType,
		ActorAgentID: actorID,
	}
	if payload != "" {
		event.PayloadJSON = &payload
	}
	return dal.CreateOrderEvent(tx, event)
}

func mapHandlerErr(err error) *base.BaseResp {
	switch {
	case err == nil:
		return ok()
	case errors.Is(err, errAlreadyAtTarget):
		return ok()
	case errors.Is(err, errOrderNotFound):
		return fail("order not found")
	case errors.Is(err, dal.ErrTransitionConflict):
		return badReq("order status changed by concurrent operation")
	case err.Error() == "not seller" || err.Error() == "not buyer":
		return badReq(err.Error())
	case strings.HasPrefix(err.Error(), "transfer verification failed:"):
		return badReq(err.Error())
	default:
		return fail("update: " + err.Error())
	}
}

func mapCreateOrderErr(err error) *base.BaseResp {
	msg := err.Error()
	switch {
	case msg == "buyer gate blocked",
		msg == "service not active",
		msg == "cannot buy own service",
		strings.HasPrefix(msg, "buyer_input validation:"):
		return badReq(msg)
	case strings.HasPrefix(msg, "service not found:"):
		return fail(msg)
	default:
		return fail(msg)
	}
}

func (s *TradeServiceImpl) saveTransferReceipt(tx *gorm.DB, orderID int64, transferID, state string, entry *chief.Entry) error {
	receiptID, err := s.receiptIDGen.NextID()
	if err != nil {
		return fmt.Errorf("receipt id gen: %w", err)
	}
	receipt := &dal.TradeTransferReceipt{
		ReceiptID:     receiptID,
		OrderID:       orderID,
		TransferID:    transferID,
		TransferState: state,
	}
	if entry != nil {
		receipt.TxHash = entry.Metadata.TxHash
		receipt.SettlementRecordID = entry.Metadata.SettlementRecordID
		receipt.Asset = entry.Asset
		if amt, perr := strconv.ParseInt(entry.AvailableDeltaAtomic, 10, 64); perr == nil {
			receipt.AmountAtomic = amt
		}
		raw, _ := json.Marshal(entry)
		s := string(raw)
		receipt.RawPayload = &s
	}
	return dal.CreateTransferReceipt(tx, receipt)
}

func (s *TradeServiceImpl) enqueueOrderEvent(tx *gorm.DB, orderID, serviceID int64, eventType int16) error {
	outID, err := s.outboxIDGen.NextID()
	if err != nil {
		return fmt.Errorf("outbox id gen: %w", err)
	}
	payload, err := dal.MarshalOrderEventPayload(outID, orderID, serviceID, dal.EventTypeName(eventType))
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}
	return dal.InsertOutbox(tx, &dal.TradeOutbox{
		OutboxID:    outID,
		StreamName:  "stream:trade:order-event",
		PayloadJSON: payload,
		CreatedAt:   time.Now().UnixMilli(),
	})
}

// --- Thrift converters ---

func svcToThrift(s *dal.TradingService, stats dal.TradingServiceStats) *trade.TradingService {
	ts := &trade.TradingService{
		ServiceId:          s.ServiceID,
		SellerAgentId:      s.SellerAgentID,
		Title:              s.Title,
		CapabilityDesc:     s.CapabilityDesc,
		CallSpecText:       s.CallSpecText,
		PriceText:          s.PriceText,
		AmountAtomic:       s.AmountAtomic,
		Asset:              s.Asset,
		DeliveryDeadlineMs: s.DeliveryDeadlineMs,
		Status:             s.Status,
		SuccessRate:        stats.SuccessRate,
		AvgLatencyMs:       stats.AvgLatencyMs,
		OrderCount:         stats.OrderCount,
		ReleasedCount:      stats.ReleasedCount,
		RefundedCount:      stats.RefundedCount,
		ExpiredCount:       stats.ExpiredCount,
		CreatedAt:          s.CreatedAt,
		UpdatedAt:          s.UpdatedAt,
	}
	if s.CallSpecSchema != nil {
		ts.CallSpecSchema = *s.CallSpecSchema
	}
	return ts
}

func orderToThrift(o *dal.TradeOrder) *trade.TradeOrder {
	to := &trade.TradeOrder{
		OrderId:                  o.OrderID,
		ServiceId:                o.ServiceID,
		BuyerAgentId:             o.BuyerAgentID,
		SellerAgentId:            o.SellerAgentID,
		Status:                   o.Status,
		TransferId:               o.TransferID,
		TransferState:            o.TransferState,
		FrozenTitle:              o.FrozenTitle,
		FrozenCallSpecText:       o.FrozenCallSpecText,
		FrozenAmountAtomic:       o.FrozenAmountAtomic,
		FrozenAsset:              o.FrozenAsset,
		FrozenDeliveryDeadlineMs: o.FrozenDeliveryDeadlineMs,
		BuyerInput:               o.BuyerInput,
		DeliveryPayload:          o.DeliveryPayload,
		CreatedAt:                o.CreatedAt,
		DeadlineAt:               o.DeadlineAt,
	}
	if o.FrozenCallSpecSchema != nil {
		to.FrozenCallSpecSchema = *o.FrozenCallSpecSchema
	}
	if o.PaidAt != nil {
		to.PaidAt = *o.PaidAt
	}
	if o.DeliveredAt != nil {
		to.DeliveredAt = *o.DeliveredAt
	}
	if o.ReleasedAt != nil {
		to.ReleasedAt = *o.ReleasedAt
	}
	if o.RefundedAt != nil {
		to.RefundedAt = *o.RefundedAt
	}
	if o.ClosedAt != nil {
		to.ClosedAt = *o.ClosedAt
	}
	return to
}

func eventsToThrift(events []*dal.TradeOrderEvent) []*trade.TradeOrderEvent {
	result := make([]*trade.TradeOrderEvent, len(events))
	for i, e := range events {
		te := &trade.TradeOrderEvent{
			EventId:      e.EventID,
			OrderId:      e.OrderID,
			EventType:    dal.EventTypeName(e.EventType),
			ActorAgentId: e.ActorAgentID,
			CreatedAt:    e.CreatedAt,
		}
		if e.PayloadJSON != nil {
			te.PayloadJson = *e.PayloadJSON
		}
		result[i] = te
	}
	return result
}
