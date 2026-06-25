package dal

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

// Service status
const (
	ServiceStatusDraft   int16 = 0
	ServiceStatusActive  int16 = 1
	ServiceStatusOffline int16 = 2
)

// Order status. Slots 1 (escrow_locked) and 4 (seller_cancelled) were retired
// when the chief escrow model was replaced by direct kovaloop transfers. Slot
// 6 (refunded) was retired when RefundOrder was removed; existing rows are
// preserved and isTerminalStatus still recognises it for historical orders,
// but no current code path emits or transitions into it.
const (
	OrderStatusCreated   int16 = 0
	OrderStatusDelivered int16 = 2
	OrderStatusReleased  int16 = 3
	OrderStatusExpired   int16 = 5
	OrderStatusRefunded  int16 = 6
)

// Order event type. Stored as SMALLINT; the string form is exposed at the API
// boundary via EventTypeName / ParseEventTypeName. Slots 2 (escrow_locked) and
// 7 (seller_cancelled) are retained in the name map so historical event rows
// still render. Slot 5 (refunded) is also historical-only after RefundOrder
// removal — the constant stays so eventTypeNames continues to map it, but no
// current code path writes EventTypeRefunded rows.
const (
	EventTypeUnknown   int16 = 0
	EventTypeCreated   int16 = 1
	EventTypeDelivered int16 = 3
	EventTypeReleased  int16 = 4
	EventTypeRefunded  int16 = 5
	EventTypeExpired   int16 = 6
)

var eventTypeNames = map[int16]string{
	EventTypeCreated:   "created",
	2:                  "escrow_locked",
	EventTypeDelivered: "delivered",
	EventTypeReleased:  "released",
	EventTypeRefunded:  "refunded",
	EventTypeExpired:   "expired",
	7:                  "seller_cancelled",
}

func EventTypeName(t int16) string {
	if n, ok := eventTypeNames[t]; ok {
		return n
	}
	return ""
}

func ParseEventTypeName(name string) int16 {
	for code, n := range eventTypeNames {
		if n == name {
			return code
		}
	}
	return EventTypeUnknown
}

// ErrTransitionConflict is returned when a status transition update affects
// zero rows, meaning the order's status has already changed (concurrent caller
// won the race or the transition was previously applied).
var ErrTransitionConflict = errors.New("order status transition conflict")

// TradingService is the slow-changing meta row. Order-driven counters and the
// "recent activity" timestamp live on TradingServiceStats; see that struct
// and the migration in migrations/000014_add_trading.sql for the rationale.
type TradingService struct {
	ServiceID          int64   `gorm:"column:service_id;primaryKey"`
	SellerAgentID      int64   `gorm:"column:seller_agent_id;not null"`
	Title              string  `gorm:"column:title;type:varchar(200);not null"`
	CapabilityDesc     string  `gorm:"column:capability_desc;type:text;not null;default:''"`
	CallSpecText       string  `gorm:"column:call_spec_text;type:text;not null;default:''"`
	CallSpecSchema     *string `gorm:"column:call_spec_schema;type:jsonb"`
	PriceText          string  `gorm:"column:price_text;type:varchar(100);not null;default:''"`
	AmountAtomic       int64   `gorm:"column:amount_atomic;not null"`
	Asset              string  `gorm:"column:asset;type:varchar(20);not null;default:'USDC'"`
	DeliveryDeadlineMs int64   `gorm:"column:delivery_deadline_ms;not null"`
	Status             int16   `gorm:"column:status;not null;default:0"`
	CreatedAt          int64   `gorm:"column:created_at;not null"`
	UpdatedAt          int64   `gorm:"column:updated_at;not null"`
	IndexedAt          int64   `gorm:"column:indexed_at;not null;default:0"`
	// LLM-enriched fields written by pipeline/consumer/service_enrich.go.
	// See docs/superpowers/specs/2026-06-09-task-to-service-search-design.md §8.
	CapabilityTags    pq.StringArray `gorm:"column:capability_tags;type:text[];not null;default:'{}'"`
	UseCases          string         `gorm:"column:use_cases;type:text;not null;default:''"`
	CanonicalInputs   string         `gorm:"column:canonical_inputs;type:jsonb;not null;default:'[]'"`
	CanonicalOutputs  string         `gorm:"column:canonical_outputs;type:jsonb;not null;default:'[]'"`
	EnrichmentVersion int            `gorm:"column:enrichment_version;not null;default:0"`
}

func (TradingService) TableName() string { return "trading_services" }

// TradingServiceStats holds the rolling counters and recency signal for a
// service. Updated by the order-event consumer; read by the ranker via
// BatchGetServiceRankStats and by the API converter via GetServiceStats.
type TradingServiceStats struct {
	ServiceID      int64   `gorm:"column:service_id;primaryKey"`
	OrderCount     int32   `gorm:"column:order_count;not null;default:0"`
	ReleasedCount  int32   `gorm:"column:released_count;not null;default:0"`
	RefundedCount  int32   `gorm:"column:refunded_count;not null;default:0"`
	ExpiredCount   int32   `gorm:"column:expired_count;not null;default:0"`
	SuccessRate    float64 `gorm:"column:success_rate;not null;default:0"`
	AvgLatencyMs   int64   `gorm:"column:avg_latency_ms;not null;default:0"`
	LastActivityAt int64   `gorm:"column:last_activity_at;not null;default:0"`
	UpdatedAt      int64   `gorm:"column:updated_at;not null"`
}

func (TradingServiceStats) TableName() string { return "trading_service_stats" }

// TradingServiceStatsDaily is a daily snapshot of trading_service_stats,
// stored in a partitioned-by-day table so old days can be detached cheaply.
// Populated by a separate snapshot cron (TBD).
type TradingServiceStatsDaily struct {
	ActivityDate   time.Time `gorm:"column:activity_date;primaryKey"`
	ServiceID      int64     `gorm:"column:service_id;primaryKey"`
	OrderCount     int32     `gorm:"column:order_count;not null;default:0"`
	ReleasedCount  int32     `gorm:"column:released_count;not null;default:0"`
	RefundedCount  int32     `gorm:"column:refunded_count;not null;default:0"`
	ExpiredCount   int32     `gorm:"column:expired_count;not null;default:0"`
	SuccessRate    float64   `gorm:"column:success_rate;not null;default:0"`
	AvgLatencyMs   int64     `gorm:"column:avg_latency_ms;not null;default:0"`
	LastActivityAt int64     `gorm:"column:last_activity_at;not null;default:0"`
	SnapshotAt     int64     `gorm:"column:snapshot_at;not null"`
}

func (TradingServiceStatsDaily) TableName() string { return "trading_service_stats_daily" }

type TradeOrder struct {
	OrderID                  int64   `gorm:"column:order_id;primaryKey"`
	ServiceID                int64   `gorm:"column:service_id;not null"`
	BuyerAgentID             int64   `gorm:"column:buyer_agent_id;not null"`
	SellerAgentID            int64   `gorm:"column:seller_agent_id;not null"`
	Status                   int16   `gorm:"column:status;not null;default:0"`
	TransferID               string  `gorm:"column:transfer_id;type:varchar(200);not null;default:''"`
	TransferState            string  `gorm:"column:transfer_state;type:varchar(20);not null;default:''"`
	FrozenTitle              string  `gorm:"column:frozen_title;type:varchar(200);not null"`
	FrozenCallSpecText       string  `gorm:"column:frozen_call_spec_text;type:text;not null;default:''"`
	FrozenCallSpecSchema     *string `gorm:"column:frozen_call_spec_schema;type:jsonb"`
	FrozenAmountAtomic       int64   `gorm:"column:frozen_amount_atomic;not null"`
	FrozenAsset              string  `gorm:"column:frozen_asset;type:varchar(20);not null"`
	FrozenDeliveryDeadlineMs int64   `gorm:"column:frozen_delivery_deadline_ms;not null"`
	BuyerInput               string  `gorm:"column:buyer_input;type:text;not null;default:''"`
	DeliveryPayload          string  `gorm:"column:delivery_payload;type:text;not null;default:''"`
	CreatedAt                int64   `gorm:"column:created_at;not null"`
	DeadlineAt               int64   `gorm:"column:deadline_at;not null"`
	PaidAt                   *int64  `gorm:"column:paid_at"`
	DeliveredAt              *int64  `gorm:"column:delivered_at"`
	ReleasedAt               *int64  `gorm:"column:released_at"`
	RefundedAt               *int64  `gorm:"column:refunded_at"`
	ClosedAt                 *int64  `gorm:"column:closed_at"`
	IdempotencyKey           string  `gorm:"column:idempotency_key;type:varchar(64);not null;default:''"`
}

func (TradeOrder) TableName() string { return "trade_orders" }

type TradeOrderEvent struct {
	EventID      int64   `gorm:"column:event_id;primaryKey"`
	OrderID      int64   `gorm:"column:order_id;not null"`
	EventType    int16   `gorm:"column:event_type;not null"`
	ActorAgentID int64   `gorm:"column:actor_agent_id;not null;default:0"`
	PayloadJSON  *string `gorm:"column:payload_json;type:jsonb"`
	CreatedAt    int64   `gorm:"column:created_at;not null"`
}

func (TradeOrderEvent) TableName() string { return "trade_order_events" }

type TradeTransferReceipt struct {
	ReceiptID          int64   `gorm:"column:receipt_id;primaryKey"`
	OrderID            int64   `gorm:"column:order_id;not null"`
	TransferID         string  `gorm:"column:transfer_id;type:varchar(200);not null;default:''"`
	Provider           string  `gorm:"column:provider;type:varchar(30);not null;default:'chief'"`
	TransferState      string  `gorm:"column:transfer_state;type:varchar(20);not null"`
	ProviderEventID    string  `gorm:"column:provider_event_id;type:varchar(200);not null;default:''"`
	TxHash             string  `gorm:"column:tx_hash;type:varchar(120);not null;default:''"`
	SettlementRecordID string  `gorm:"column:settlement_record_id;type:varchar(120);not null;default:''"`
	Asset              string  `gorm:"column:asset;type:varchar(20);not null;default:''"`
	AmountAtomic       int64   `gorm:"column:amount_atomic;not null;default:0"`
	RawPayload         *string `gorm:"column:raw_payload;type:jsonb"`
	CreatedAt          int64   `gorm:"column:created_at;not null"`
}

func (TradeTransferReceipt) TableName() string { return "trade_transfer_receipts" }

// TradeOutbox is the transactional outbox row for an MQ event that must
// reach a Redis Stream. Handlers insert rows in the same transaction that
// mutates trade_orders; the dispatcher cron polls pending rows and publishes
// them. See docs/superpowers/specs/2026-06-11-trade-consistency-and-idempotency-design.md.
type TradeOutbox struct {
	OutboxID    int64  `gorm:"column:outbox_id;primaryKey"`
	StreamName  string `gorm:"column:stream_name;type:varchar(64);not null"`
	PayloadJSON string `gorm:"column:payload_json;type:jsonb;not null"`
	Status      int16  `gorm:"column:status;not null;default:0"`
	CreatedAt   int64  `gorm:"column:created_at;not null"`
	PublishedAt *int64 `gorm:"column:published_at"`
}

func (TradeOutbox) TableName() string { return "trade_outbox" }

// --- Service DAL ---

func CreateService(db *gorm.DB, svc *TradingService) error {
	now := time.Now().UnixMilli()
	svc.CreatedAt = now
	svc.UpdatedAt = now
	svc.Status = ServiceStatusActive
	if svc.CanonicalInputs == "" {
		svc.CanonicalInputs = "[]"
	}
	if svc.CanonicalOutputs == "" {
		svc.CanonicalOutputs = "[]"
	}
	return db.Create(svc).Error
}

func GetService(db *gorm.DB, serviceID int64) (*TradingService, error) {
	var svc TradingService
	if err := db.Where("service_id = ?", serviceID).First(&svc).Error; err != nil {
		return nil, err
	}
	return &svc, nil
}

func UpdateService(db *gorm.DB, serviceID, sellerAgentID int64, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now().UnixMilli()
	return db.Model(&TradingService{}).
		Where("service_id = ? AND seller_agent_id = ?", serviceID, sellerAgentID).
		Updates(updates).Error
}

func OfflineService(db *gorm.DB, serviceID, sellerAgentID int64) error {
	return UpdateService(db, serviceID, sellerAgentID, map[string]interface{}{
		"status": ServiceStatusOffline,
	})
}

func ListServicesBySeller(db *gorm.DB, sellerAgentID int64, limit int, cursor int64) ([]*TradingService, error) {
	var services []*TradingService
	q := db.Where("seller_agent_id = ?", sellerAgentID)
	if cursor > 0 {
		q = q.Where("service_id < ?", cursor)
	}
	if err := q.Order("service_id DESC").Limit(limit).Find(&services).Error; err != nil {
		return nil, err
	}
	return services, nil
}

// UpdateServiceEnrichment writes the LLM-enriched fields (capability_tags,
// use_cases, canonical_inputs, canonical_outputs, enrichment_version) for a
// service. Called by the pipeline ServiceConsumer after each successful
// LLM enrichment pass.
func UpdateServiceEnrichment(db *gorm.DB, serviceID int64, capTags []string, useCases, canonicalInputs, canonicalOutputs string, version int) error {
	return db.Model(&TradingService{}).
		Where("service_id = ?", serviceID).
		Updates(map[string]interface{}{
			"capability_tags":    pq.StringArray(capTags),
			"use_cases":          useCases,
			"canonical_inputs":   canonicalInputs,
			"canonical_outputs":  canonicalOutputs,
			"enrichment_version": version,
			"updated_at":         time.Now().UnixMilli(),
		}).Error
}

// --- Order DAL ---

func CreateOrder(db *gorm.DB, order *TradeOrder) error {
	order.CreatedAt = time.Now().UnixMilli()
	order.DeadlineAt = order.CreatedAt + order.FrozenDeliveryDeadlineMs
	return db.Create(order).Error
}

func GetOrder(db *gorm.DB, orderID int64) (*TradeOrder, error) {
	var order TradeOrder
	if err := db.Where("order_id = ?", orderID).First(&order).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

// FindOrderByIdempotencyKey returns the existing order for (buyerAgentID, key),
// or gorm.ErrRecordNotFound if none. Callers use this to fast-path retries
// without opening a transaction.
func FindOrderByIdempotencyKey(db *gorm.DB, buyerAgentID int64, key string) (*TradeOrder, error) {
	if key == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var order TradeOrder
	if err := db.Where("buyer_agent_id = ? AND idempotency_key = ?", buyerAgentID, key).First(&order).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

func UpdateOrderStatus(db *gorm.DB, orderID int64, updates map[string]interface{}) error {
	return db.Model(&TradeOrder{}).Where("order_id = ?", orderID).Updates(updates).Error
}

// TransitionOrderStatus atomically updates an order's status from `fromStatus`
// to `toStatus`, returning ErrTransitionConflict if no row matched (i.e. the
// order is no longer in `fromStatus`). The transition guard makes retries safe:
// a duplicate call observes zero rows affected instead of overwriting a
// terminal state. Extra column updates may be supplied via `extra`; `status`
// is set by this function and must not appear in `extra`.
func TransitionOrderStatus(db *gorm.DB, orderID int64, fromStatus, toStatus int16, extra map[string]interface{}) error {
	updates := map[string]interface{}{"status": toStatus}
	for k, v := range extra {
		if k == "status" {
			continue
		}
		updates[k] = v
	}
	res := db.Model(&TradeOrder{}).
		Where("order_id = ? AND status = ?", orderID, fromStatus).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrTransitionConflict
	}
	return nil
}

func ListOrdersByAgent(db *gorm.DB, agentID int64, role string, statusFilter int16, limit int, cursor int64) ([]*TradeOrder, error) {
	var orders []*TradeOrder
	q := db.Model(&TradeOrder{})
	if role == "buyer" {
		q = q.Where("buyer_agent_id = ?", agentID)
	} else {
		q = q.Where("seller_agent_id = ?", agentID)
	}
	if statusFilter >= 0 {
		q = q.Where("status = ?", statusFilter)
	}
	if cursor > 0 {
		q = q.Where("order_id < ?", cursor)
	}
	if err := q.Order("order_id DESC").Limit(limit).Find(&orders).Error; err != nil {
		return nil, err
	}
	return orders, nil
}

func CountActiveOrders(db *gorm.DB, buyerAgentID int64) (int64, error) {
	var count int64
	err := db.Model(&TradeOrder{}).
		Where("buyer_agent_id = ? AND status IN ?", buyerAgentID, []int16{
			OrderStatusCreated, OrderStatusDelivered,
		}).Count(&count).Error
	return count, err
}

func HasPendingRelease(db *gorm.DB, buyerAgentID int64) (bool, error) {
	var count int64
	err := db.Model(&TradeOrder{}).
		Where("buyer_agent_id = ? AND status = ?", buyerAgentID, OrderStatusDelivered).
		Count(&count).Error
	return count > 0, err
}

// CountUnpaidOrders returns the number of delivered-but-unreleased orders
// for the buyer. The buyer gate uses this to surface "has_unpaid_orders" as
// the block reason; any value > 0 blocks new order creation.
func CountUnpaidOrders(db *gorm.DB, buyerAgentID int64) (int64, error) {
	var count int64
	err := db.Model(&TradeOrder{}).
		Where("buyer_agent_id = ? AND status = ?", buyerAgentID, OrderStatusDelivered).
		Count(&count).Error
	return count, err
}

func FindExpiredOrders(db *gorm.DB, nowMs int64, limit int) ([]*TradeOrder, error) {
	var orders []*TradeOrder
	err := db.Where("status = ? AND deadline_at < ?", OrderStatusCreated, nowMs).
		Limit(limit).Find(&orders).Error
	return orders, err
}

// --- Event DAL ---

func CreateOrderEvent(db *gorm.DB, event *TradeOrderEvent) error {
	event.CreatedAt = time.Now().UnixMilli()
	return db.Create(event).Error
}

func ListOrderEvents(db *gorm.DB, orderID int64) ([]*TradeOrderEvent, error) {
	var events []*TradeOrderEvent
	err := db.Where("order_id = ?", orderID).Order("created_at ASC").Find(&events).Error
	return events, err
}

// --- Transfer Receipt DAL ---

func CreateTransferReceipt(db *gorm.DB, receipt *TradeTransferReceipt) error {
	receipt.CreatedAt = time.Now().UnixMilli()
	return db.Create(receipt).Error
}

// --- Outbox DAL ---

func InsertOutbox(db *gorm.DB, row *TradeOutbox) error {
	if row.CreatedAt == 0 {
		row.CreatedAt = time.Now().UnixMilli()
	}
	return db.Create(row).Error
}

// ListPendingOutbox returns pending (status=0) rows in outbox_id order so the
// dispatcher publishes events in roughly the order they were produced.
func ListPendingOutbox(db *gorm.DB, limit int) ([]*TradeOutbox, error) {
	var rows []*TradeOutbox
	err := db.Where("status = ?", int16(0)).Order("outbox_id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

func MarkOutboxPublished(db *gorm.DB, outboxID int64, nowMs int64) error {
	return db.Model(&TradeOutbox{}).
		Where("outbox_id = ?", outboxID).
		Updates(map[string]interface{}{
			"status":       int16(1),
			"published_at": nowMs,
		}).Error
}

// DeleteOldPublishedOutbox removes published rows with published_at < beforeMs.
// Returns the number of rows deleted.
func DeleteOldPublishedOutbox(db *gorm.DB, beforeMs int64) (int64, error) {
	res := db.Where("status = ? AND published_at < ?", int16(1), beforeMs).Delete(&TradeOutbox{})
	return res.RowsAffected, res.Error
}

// OrderEventPayload is the canonical JSON shape published on
// stream:trade:order-event. Terminal events (released/refunded/expired) use
// only the first four fields; created and delivered events carry the extra
// agent ids and frozen snapshot the consumer needs to write the
// trade:notify:{agent_id} Redis hash without re-reading trade_orders.
type OrderEventPayload struct {
	OutboxID               string `json:"outbox_id"`
	OrderID                string `json:"order_id"`
	ServiceID              string `json:"service_id"`
	EventType              string `json:"event_type"`
	BuyerAgentID           string `json:"buyer_agent_id,omitempty"`
	SellerAgentID          string `json:"seller_agent_id,omitempty"`
	FrozenTitle            string `json:"frozen_title,omitempty"`
	BuyerInput             string `json:"buyer_input,omitempty"`
	DeliveryPayloadPreview string `json:"delivery_payload_preview,omitempty"`
	FrozenAmountAtomic     string `json:"frozen_amount_atomic,omitempty"`
	FrozenAsset            string `json:"frozen_asset,omitempty"`
	NowMs                  string `json:"now_ms,omitempty"`
}

// MarshalOrderEventPayload renders the JSON for terminal events
// (released/refunded/expired). Used by handler.go and pipeline/cron/trade_expiry.go.
func MarshalOrderEventPayload(outboxID, orderID, serviceID int64, eventType string) (string, error) {
	return marshalOrderPayload(OrderEventPayload{
		OutboxID:  strconv.FormatInt(outboxID, 10),
		OrderID:   strconv.FormatInt(orderID, 10),
		ServiceID: strconv.FormatInt(serviceID, 10),
		EventType: eventType,
	})
}

// MarshalOrderCreatedPayload renders the JSON for a `created` event.
func MarshalOrderCreatedPayload(outboxID, orderID, serviceID, buyerAgentID int64, frozenTitle, buyerInput string, nowMs int64) (string, error) {
	return marshalOrderPayload(OrderEventPayload{
		OutboxID:     strconv.FormatInt(outboxID, 10),
		OrderID:      strconv.FormatInt(orderID, 10),
		ServiceID:    strconv.FormatInt(serviceID, 10),
		EventType:    "created",
		BuyerAgentID: strconv.FormatInt(buyerAgentID, 10),
		FrozenTitle:  frozenTitle,
		BuyerInput:   buyerInput,
		NowMs:        strconv.FormatInt(nowMs, 10),
	})
}

// MarshalOrderDeliveredPayload renders the JSON for a `delivered` event.
func MarshalOrderDeliveredPayload(outboxID, orderID, serviceID, sellerAgentID int64, frozenTitle, deliveryPreview, frozenAsset string, frozenAmountAtomic, nowMs int64) (string, error) {
	return marshalOrderPayload(OrderEventPayload{
		OutboxID:               strconv.FormatInt(outboxID, 10),
		OrderID:                strconv.FormatInt(orderID, 10),
		ServiceID:              strconv.FormatInt(serviceID, 10),
		EventType:              "delivered",
		SellerAgentID:          strconv.FormatInt(sellerAgentID, 10),
		FrozenTitle:            frozenTitle,
		DeliveryPayloadPreview: deliveryPreview,
		FrozenAmountAtomic:     strconv.FormatInt(frozenAmountAtomic, 10),
		FrozenAsset:            frozenAsset,
		NowMs:                  strconv.FormatInt(nowMs, 10),
	})
}

func marshalOrderPayload(p OrderEventPayload) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// --- Stats Update ---

// IncrementServiceStats UPSERTs the per-service stats row, bumping the named
// terminal counter (released_count / refunded_count / expired_count) and
// order_count by one, and refreshing last_activity_at + updated_at to nowMs.
// The first event for a service inserts the row; subsequent events update it.
func IncrementServiceStats(db *gorm.DB, serviceID int64, column string, nowMs int64) error {
	allowed := map[string]bool{
		"released_count": true,
		"refunded_count": true,
		"expired_count":  true,
	}
	if !allowed[column] {
		return fmt.Errorf("invalid stats column: %s", column)
	}
	sql := `
		INSERT INTO trading_service_stats
			(service_id, order_count, ` + column + `, last_activity_at, updated_at)
		VALUES (?, 1, 1, ?, ?)
		ON CONFLICT (service_id) DO UPDATE SET
			order_count       = trading_service_stats.order_count + 1,
			` + column + ` = trading_service_stats.` + column + ` + 1,
			last_activity_at  = EXCLUDED.last_activity_at,
			updated_at        = EXCLUDED.updated_at
	`
	return db.Exec(sql, serviceID, nowMs, nowMs).Error
}

// GetServiceStats reads the stats row for a single service. Returns a
// zero-valued struct (not an error) if no row exists yet.
func GetServiceStats(db *gorm.DB, serviceID int64) (TradingServiceStats, error) {
	var stats TradingServiceStats
	err := db.Where("service_id = ?", serviceID).Take(&stats).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TradingServiceStats{ServiceID: serviceID}, nil
		}
		return TradingServiceStats{}, err
	}
	return stats, nil
}

// BatchGetServiceStats returns the full stats row for each requested service.
// Missing rows are reported as zero-valued entries so callers can rely on a
// stable map shape for serialization.
func BatchGetServiceStats(db *gorm.DB, serviceIDs []int64) (map[int64]TradingServiceStats, error) {
	out := make(map[int64]TradingServiceStats, len(serviceIDs))
	if len(serviceIDs) == 0 {
		return out, nil
	}
	var rows []TradingServiceStats
	if err := db.Where("service_id IN ?", serviceIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ServiceID] = r
	}
	for _, id := range serviceIDs {
		if _, ok := out[id]; !ok {
			out[id] = TradingServiceStats{ServiceID: id}
		}
	}
	return out, nil
}

// UpdateServiceSuccessRate recomputes success_rate from the persisted counters
// on the stats row. A no-op (returns nil) if the stats row does not yet exist.
func UpdateServiceSuccessRate(db *gorm.DB, serviceID int64) error {
	return db.Exec(`
		UPDATE trading_service_stats
		SET success_rate = CASE WHEN order_count > 0
			THEN released_count::float / order_count
			ELSE 0 END,
		    updated_at = ?
		WHERE service_id = ?
	`, time.Now().UnixMilli(), serviceID).Error
}
