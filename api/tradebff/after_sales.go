package tradebff

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Service) TradeOrderAfterSales(ctx context.Context, c *app.RequestContext) {
	actor, orderID, ok := orderInputActor(c)
	if !ok {
		return
	}
	var request struct {
		Kind    string `json:"kind"`
		Reason  string `json:"reason"`
		OrderID string `json:"order_id"`
	}
	if !decodeOrderInputBody(c, &request, "order_id") {
		replyError(c, http.StatusBadRequest, "INVALID_AFTER_SALES_REQUEST", "售后申请内容无效")
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if (request.Kind != "refund" && request.Kind != "complaint") || request.Reason == "" || len(request.Reason) > 2000 || strings.ContainsRune(request.Reason, 0) {
		replyError(c, http.StatusBadRequest, "INVALID_AFTER_SALES_REQUEST", "请选择售后类型并填写原因")
		return
	}
	if s == nil || s.client == nil || s.delegator == nil {
		s.unavailable(c)
		return
	}
	request.OrderID = orderID
	body, err := json.Marshal(request)
	if err != nil {
		replyError(c, http.StatusBadRequest, "INVALID_AFTER_SALES_REQUEST", "售后申请内容无效")
		return
	}
	_, err = s.fetch(ctx, actor, "orders:write", "console.trade.orders.after_sales.create", http.MethodPost,
		"/api/v1/orders/"+orderID+"/after-sales", nil, body, string(c.GetHeader("Idempotency-Key")), true)
	if err != nil {
		status := upstreamStatus(err)
		var upstream *UpstreamError
		if errors.As(err, &upstream) && upstream.Status == http.StatusBadRequest {
			status = http.StatusBadRequest
		}
		replyError(c, status, "AFTER_SALES_REQUEST_FAILED", "售后申请未提交，请刷新订单后重试")
		return
	}
	reply(c, http.StatusOK, map[string]bool{"submitted": true})
}
