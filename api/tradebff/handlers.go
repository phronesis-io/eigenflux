package tradebff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

const unavailableReason = "Commission 交易服务尚未配置可信主体委托"

type Config struct {
	Endpoint             string
	DelegationKeyID      string
	DelegationPrivateKey string
}

type Service struct {
	client    *CommissionClient
	delegator *Delegator
	reason    string
}

func New(config Config) (*Service, error) {
	client, err := NewCommissionClient(config.Endpoint)
	if err != nil {
		return nil, err
	}
	delegator, err := NewDelegator(config.DelegationKeyID, config.DelegationPrivateKey)
	if err != nil {
		return nil, err
	}
	return &Service{client: client, delegator: delegator}, nil
}

func NewUnavailable(reason string) *Service {
	if strings.TrimSpace(reason) == "" {
		reason = unavailableReason
	}
	return &Service{reason: reason}
}

func agentID(c *app.RequestContext) (int64, bool) {
	value, exists := c.Get("agent_id")
	identifier, ok := value.(int64)
	return identifier, exists && ok && identifier > 0
}

func reply(c *app.RequestContext, status int, data interface{}) {
	c.Header("Cache-Control", "private, no-store")
	c.JSON(status, map[string]interface{}{"data": data})
}

func replyError(c *app.RequestContext, status int, code, message string) {
	c.Header("Cache-Control", "private, no-store")
	c.JSON(status, map[string]interface{}{"error": map[string]interface{}{"code": code, "message": message}})
}

func (s *Service) unavailable(c *app.RequestContext) {
	reason := ""
	if s != nil {
		reason = s.reason
	}
	if reason == "" {
		reason = unavailableReason
	}
	replyError(c, http.StatusServiceUnavailable, "COMMISSION_UNAVAILABLE", reason)
}

func (s *Service) fetch(ctx context.Context, agentID int64, scope, operation, method, path string, query url.Values, body []byte, idempotencyKey string, bindMutation bool) (json.RawMessage, error) {
	if s == nil || s.client == nil || s.delegator == nil {
		return nil, fmt.Errorf("Commission BFF is not configured")
	}
	token, err := s.delegator.Token(DelegationRequest{AgentID: agentID, Scope: scope, Method: method, Operation: operation, Body: body, IdempotencyKey: idempotencyKey, BindMutation: bindMutation})
	if err != nil {
		return nil, err
	}
	return s.client.Do(ctx, token, method, path, query, body, idempotencyKey)
}

func (s *Service) proxy(ctx context.Context, c *app.RequestContext, scope, operation, method, path string, query url.Values, body []byte, bindMutation bool) {
	identifier, ok := agentID(c)
	if !ok {
		replyError(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console Session 无效")
		return
	}
	if s == nil || s.client == nil || s.delegator == nil {
		s.unavailable(c)
		return
	}
	key := string(c.GetHeader("Idempotency-Key"))
	if bindMutation && strings.TrimSpace(key) == "" {
		replyError(c, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key 不能为空")
		return
	}
	data, err := s.fetch(ctx, identifier, scope, operation, method, path, query, body, key, bindMutation)
	if err != nil {
		status := upstreamStatus(err)
		replyError(c, status, "COMMISSION_REQUEST_FAILED", http.StatusText(status))
		return
	}
	reply(c, http.StatusOK, data)
}

func (s *Service) TradeOverview(ctx context.Context, c *app.RequestContext) {
	identifier, ok := agentID(c)
	if !ok {
		replyError(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console Session 无效")
		return
	}
	if s == nil || s.client == nil || s.delegator == nil {
		s.unavailable(c)
		return
	}
	type result struct {
		data json.RawMessage
		err  error
	}
	summaryResult, balanceResult := make(chan result, 1), make(chan result, 1)
	go func() {
		data, err := s.fetch(ctx, identifier, "trade:summary:read", "trade.summary.read", http.MethodGet, "/api/v1/trade/summary", nil, nil, "", false)
		summaryResult <- result{data, err}
	}()
	go func() {
		data, err := s.fetch(ctx, identifier, "wallet:read", "wallet.balance.read", http.MethodGet, "/api/v1/wallet/balance", nil, nil, "", false)
		balanceResult <- result{data, err}
	}()
	summary, balance := <-summaryResult, <-balanceResult
	if balance.err != nil {
		replyError(c, upstreamStatus(balance.err), "COMMISSION_BALANCE_UNAVAILABLE", "钱包余额暂不可用")
		return
	}
	out := map[string]interface{}{"capabilities_count": nil, "accepted_orders_count": nil, "outgoing_orders_count": nil}
	mergeNested(out, balance.data, "balance")
	if summary.err == nil {
		mergeNested(out, summary.data, "summary")
	} else {
		out["warnings"] = []map[string]string{{"code": "TRADE_SUMMARY_UNAVAILABLE"}}
	}
	reply(c, http.StatusOK, out)
}

func (s *Service) TradeCommissions(ctx context.Context, c *app.RequestContext) {
	s.proxy(ctx, c, "commissions:mine:read", "console.trade.commissions.list", http.MethodGet, "/api/v2/console/trade/commissions", selectedQuery(c, "status", "cursor", "limit"), nil, false)
}

func (s *Service) TradeOrders(ctx context.Context, c *app.RequestContext) {
	s.proxy(ctx, c, "orders:read", "console.trade.orders.list", http.MethodGet, "/api/v2/console/trade/orders", selectedQuery(c, "role", "state", "cursor", "limit"), nil, false)
}

func (s *Service) TradeOrder(ctx context.Context, c *app.RequestContext) {
	orderID := string(c.Param("order_id"))
	if !positiveDecimal(orderID) {
		replyError(c, http.StatusBadRequest, "INVALID_ORDER_ID", "订单号无效")
		return
	}
	s.proxy(ctx, c, "orders:read", "console.trade.orders.get", http.MethodGet, "/api/v2/console/trade/orders/"+url.PathEscape(orderID), nil, nil, false)
}

func (s *Service) EarningsSummary(ctx context.Context, c *app.RequestContext) {
	s.proxy(ctx, c, "wallet:read", "wallet.balance.read", http.MethodGet, "/api/v1/wallet/balance", nil, nil, false)
}

func (s *Service) EarningsRecords(ctx context.Context, c *app.RequestContext) {
	identifier, ok := agentID(c)
	if !ok {
		replyError(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console Session 无效")
		return
	}
	if s == nil || s.client == nil || s.delegator == nil {
		s.unavailable(c)
		return
	}
	recordType := strings.TrimSpace(string(c.Query("type")))
	if recordType == "" {
		recordType = "all"
	}
	if recordType != "all" && recordType != "income" && recordType != "withdrawal" {
		replyError(c, http.StatusBadRequest, "INVALID_RECORD_TYPE", "资金记录类型无效")
		return
	}
	query := selectedQuery(c, "cursor", "limit")
	if recordType == "income" {
		data, err := s.fetch(ctx, identifier, "wallet:read", "wallet.credits.list", http.MethodGet, "/api/v1/wallet/credits", query, nil, "", false)
		s.replyIncomeRecords(c, data, err, true)
		return
	}
	if recordType == "withdrawal" {
		data, err := s.fetch(ctx, identifier, "withdrawals:read", "wallet.withdrawals.list", http.MethodGet, "/api/v1/wallet/withdrawals", query, nil, "", false)
		s.replyWithdrawalRecords(c, data, err, true)
		return
	}
	// The combined tab is intentionally first-page only. Advancing two
	// independent upstream cursors after emitting one merged page loses data.
	query.Del("cursor")
	credits, creditErr := s.fetch(ctx, identifier, "wallet:read", "wallet.credits.list", http.MethodGet, "/api/v1/wallet/credits", query, nil, "", false)
	withdrawals, withdrawalErr := s.fetch(ctx, identifier, "withdrawals:read", "wallet.withdrawals.list", http.MethodGet, "/api/v1/wallet/withdrawals", query, nil, "", false)
	if creditErr != nil || withdrawalErr != nil {
		err := creditErr
		if err == nil {
			err = withdrawalErr
		}
		replyError(c, upstreamStatus(err), "COMMISSION_RECORDS_UNAVAILABLE", "资金记录暂不可用")
		return
	}
	items := append(incomeItems(credits), withdrawalItems(withdrawals)...)
	sort.SliceStable(items, func(i, j int) bool {
		return numberValue(items[i]["occurred_at"]) > numberValue(items[j]["occurred_at"])
	})
	limit := queryLimit(query)
	if len(items) > limit {
		items = items[:limit]
	}
	reply(c, http.StatusOK, map[string]interface{}{"items": items, "next_cursor": ""})
}

func (s *Service) PayoutMethod(ctx context.Context, c *app.RequestContext) {
	identifier, ok := agentID(c)
	if !ok {
		replyError(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console Session 无效")
		return
	}
	if s == nil || s.client == nil || s.delegator == nil {
		s.unavailable(c)
		return
	}
	data, err := s.fetch(ctx, identifier, "payout:read", "wallet.binding.read", http.MethodGet, "/api/v1/wallet/binding", nil, nil, "", false)
	if err != nil {
		status := upstreamStatus(err)
		replyError(c, status, "COMMISSION_REQUEST_FAILED", http.StatusText(status))
		return
	}
	object := decodeObject(data)
	reply(c, http.StatusOK, map[string]interface{}{"method": normalizeBinding(object["binding"])})
}

func (s *Service) MutatePayoutMethod(ctx context.Context, c *app.RequestContext) {
	s.proxy(ctx, c, "payout:bind", "wallet.binding.bind", http.MethodPost, "/api/v1/wallet/binding", nil, append([]byte(nil), c.Request.Body()...), true)
}

func (s *Service) CreateWithdrawal(ctx context.Context, c *app.RequestContext) {
	s.proxy(ctx, c, "withdrawals:create", "wallet.withdrawals.create", http.MethodPost, "/api/v1/wallet/withdrawals", nil, append([]byte(nil), c.Request.Body()...), true)
}

func (s *Service) Withdrawal(ctx context.Context, c *app.RequestContext) {
	withdrawalID := string(c.Param("withdrawal_id"))
	if !positiveDecimal(withdrawalID) {
		replyError(c, http.StatusBadRequest, "INVALID_WITHDRAWAL_ID", "提现单号无效")
		return
	}
	s.proxy(ctx, c, "withdrawals:read", "wallet.withdrawals.get", http.MethodGet, "/api/v1/wallet/withdrawals/"+url.PathEscape(withdrawalID), nil, nil, false)
}

func (s *Service) replyIncomeRecords(c *app.RequestContext, data json.RawMessage, err error, includeCursor bool) {
	if err != nil {
		replyError(c, upstreamStatus(err), "COMMISSION_RECORDS_UNAVAILABLE", "收益记录暂不可用")
		return
	}
	object := decodeObject(data)
	next := ""
	if includeCursor {
		next = stringValue(object["next_cursor"])
	}
	reply(c, http.StatusOK, map[string]interface{}{"items": incomeItems(data), "next_cursor": next})
}

func (s *Service) replyWithdrawalRecords(c *app.RequestContext, data json.RawMessage, err error, includeCursor bool) {
	if err != nil {
		replyError(c, upstreamStatus(err), "COMMISSION_RECORDS_UNAVAILABLE", "提现记录暂不可用")
		return
	}
	object := decodeObject(data)
	next := ""
	if includeCursor {
		next = stringValue(object["next_cursor"])
	}
	reply(c, http.StatusOK, map[string]interface{}{"items": withdrawalItems(data), "next_cursor": next})
}

func incomeItems(data json.RawMessage) []map[string]interface{} {
	items := objectItems(data, "credits")
	for _, item := range items {
		item["record_type"] = "income"
		item["record_id"] = stringValue(item["credit_id"])
		item["status"] = item["settlement_state"]
		item["title"] = item["title_snapshot"]
	}
	return items
}

func withdrawalItems(data json.RawMessage) []map[string]interface{} {
	items := objectItems(data, "withdrawals")
	for _, item := range items {
		item["record_type"] = "withdrawal"
		item["withdrawal_id"] = stringValue(item["withdrawal_id"])
		item["record_id"] = item["withdrawal_id"]
		item["status"] = item["state"]
		item["occurred_at"] = item["created_at"]
		item["provider_operation_ref"] = item["provider_operation_reference"]
	}
	return items
}

func objectItems(data json.RawMessage, key string) []map[string]interface{} {
	object := decodeObject(data)
	raw, _ := object[key].([]interface{})
	items := make([]map[string]interface{}, 0, len(raw))
	for _, entry := range raw {
		if item, ok := entry.(map[string]interface{}); ok {
			items = append(items, item)
		}
	}
	return items
}

func decodeObject(data json.RawMessage) map[string]interface{} {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]interface{}
	if decoder.Decode(&object) != nil || object == nil {
		return map[string]interface{}{}
	}
	return object
}

func mergeNested(target map[string]interface{}, data json.RawMessage, key string) {
	object := decodeObject(data)
	if nested, ok := object[key].(map[string]interface{}); ok {
		for name, value := range nested {
			target[name] = value
		}
	}
}

func selectedQuery(c *app.RequestContext, names ...string) url.Values {
	values := make(url.Values)
	for _, name := range names {
		if value := strings.TrimSpace(string(c.Query(name))); value != "" {
			values.Set(name, value)
		}
	}
	return values
}

func positiveDecimal(value string) bool {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == strings.TrimSpace(value)
}

func queryLimit(values url.Values) int {
	limit, err := strconv.Atoi(values.Get("limit"))
	if err != nil || limit <= 0 || limit > 100 {
		return 20
	}
	return limit
}

func stringValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func numberValue(value interface{}) int64 {
	switch typed := value.(type) {
	case json.Number:
		result, _ := typed.Int64()
		return result
	case float64:
		return int64(typed)
	case int64:
		return typed
	default:
		return 0
	}
}

func normalizeBinding(value interface{}) interface{} {
	binding, ok := value.(map[string]interface{})
	if !ok {
		return nil
	}
	for _, field := range []string{"cooling_until", "updated_at"} {
		milliseconds := numberValue(binding[field])
		if milliseconds > 0 {
			binding[field] = time.UnixMilli(milliseconds).UTC().Format(time.RFC3339)
		} else {
			binding[field] = nil
		}
	}
	return binding
}
