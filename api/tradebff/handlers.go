package tradebff

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

const unavailableReason = "Commission 尚未提供 Console V2 会话到资金账户的可信身份委托契约"

type Service struct{}

func New() *Service { return &Service{} }

func agentID(c *app.RequestContext) (int64, bool) {
	value, exists := c.Get("agent_id")
	identifier, ok := value.(int64)
	return identifier, exists && ok && identifier > 0
}

func reply(c *app.RequestContext, status int, data interface{}) {
	c.Header("Cache-Control", "private, no-store")
	c.JSON(status, map[string]interface{}{"data": data})
}

func unavailable(c *app.RequestContext, capability string) {
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
		"error": map[string]interface{}{
			"code":    "COMMISSION_IDENTITY_DELEGATION_REQUIRED",
			"message": unavailableReason,
			"details": map[string]interface{}{"capability": capability},
		},
	})
}

func base() map[string]interface{} {
	return map[string]interface{}{
		"available":          false,
		"unavailable_reason": unavailableReason,
		"todo_code":          "COMMISSION_IDENTITY_DELEGATION_REQUIRED",
	}
}

func (s *Service) TradeOverview(_ context.Context, c *app.RequestContext) {
	_, ok := agentID(c)
	if !ok {
		unavailable(c, "trade.overview")
		return
	}
	data := base()
	data["capabilities_count"] = nil
	data["accepted_orders_count"] = nil
	data["outgoing_orders_count"] = nil
	data["total_fen"] = nil
	data["withdrawable_fen"] = nil
	reply(c, http.StatusOK, data)
}

func (s *Service) TradeCommissions(_ context.Context, c *app.RequestContext) {
	data := base()
	data["items"] = []interface{}{}
	data["next_cursor"] = ""
	reply(c, http.StatusOK, data)
}

func (s *Service) TradeOrders(_ context.Context, c *app.RequestContext) {
	data := base()
	data["role"] = string(c.Query("role"))
	data["items"] = []interface{}{}
	data["next_cursor"] = ""
	reply(c, http.StatusOK, data)
}

func (s *Service) TradeOrder(_ context.Context, c *app.RequestContext) {
	unavailable(c, "trade.order_detail")
}

func (s *Service) EarningsSummary(_ context.Context, c *app.RequestContext) {
	data := base()
	data["total_fen"] = nil
	data["unmatured_fen"] = nil
	data["reserved_fen"] = nil
	data["withdrawn_fen"] = nil
	data["withdrawable_fen"] = nil
	reply(c, http.StatusOK, data)
}

func (s *Service) EarningsRecords(_ context.Context, c *app.RequestContext) {
	data := base()
	data["items"] = []interface{}{}
	data["next_cursor"] = ""
	reply(c, http.StatusOK, data)
}

func (s *Service) PayoutMethod(_ context.Context, c *app.RequestContext) {
	data := base()
	data["method"] = nil
	reply(c, http.StatusOK, data)
}

func (s *Service) MutatePayoutMethod(_ context.Context, c *app.RequestContext) {
	unavailable(c, "payout_method.bind")
}

func (s *Service) CreateWithdrawal(_ context.Context, c *app.RequestContext) {
	unavailable(c, "withdrawal.create")
}

func (s *Service) Withdrawal(_ context.Context, c *app.RequestContext) {
	unavailable(c, "withdrawal.detail")
}
