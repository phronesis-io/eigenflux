package tradebff

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
)

//go:embed order_confirmation.html
var confirmationHTML string
var confirmationPage = template.Must(template.New("confirm").Parse(confirmationHTML))

func (s *Service) ConfirmOrderPayment(ctx context.Context, c *app.RequestContext) {
	id := c.Param("order_id")
	var fields struct {
		Order  string `json:"order_id"`
		Amount int64  `json:"amount_fen"`
	}
	if !positiveDecimal(id) || json.Unmarshal(c.Request.Body(), &fields) != nil || fields.Order != id || fields.Amount <= 0 {
		replyError(c, 400, "INVALID_CONFIRMATION", "订单号或金额无效")
		return
	}
	s.proxy(ctx, c, "orders:pay", "order.payment.confirm", http.MethodPost, "/api/v1/orders/"+id+"/payment/confirm", nil, c.Request.Body(), true)
}

func (s *Service) OrderPaymentPage(ctx context.Context, c *app.RequestContext) {
	id := c.Param("order_id")
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	if !positiveDecimal(id) {
		replyError(c, 400, "INVALID_ORDER_ID", "订单号无效")
		return
	}
	actor, ok := agentID(c)
	if !ok {
		replyError(c, 401, "CONSOLE_SESSION_REQUIRED", "请先登录 Console")
		return
	}
	data, err := s.fetch(ctx, actor, "orders:read", "console.trade.orders.get", "GET", "/api/v2/console/trade/orders/"+id, nil, nil, "", false)
	if err != nil {
		replyError(c, upstreamStatus(err), "COMMISSION_REQUEST_FAILED", "无法读取订单")
		return
	}
	var order struct {
		ID          string `json:"order_id"`
		Role, State string
		Contract    struct {
			Title, Currency string
			Amount          int64 `json:"gross_amount_fen"`
		}
	}
	if json.Unmarshal(data, &order) != nil || order.ID != id || order.Contract.Currency != "CNY" || order.Contract.Amount <= 0 {
		replyError(c, 502, "INVALID_ORDER", "订单金额无效")
		return
	}
	if order.Role != "buyer" || order.State != "pending_payment" {
		replyError(c, 409, "ORDER_NOT_PAYABLE", "此账号当前无法支付该订单")
		return
	}
	var page bytes.Buffer
	err = confirmationPage.Execute(&page, map[string]string{"Order": id, "Title": order.Contract.Title, "Price": fmt.Sprintf("%d.%02d", order.Contract.Amount/100, order.Contract.Amount%100), "Amount": strconv.FormatInt(order.Contract.Amount, 10), "CSRF": consolePageCSRF(c)})
	if err != nil {
		replyError(c, 500, "PAGE_UNAVAILABLE", "无法显示确认页面")
		return
	}
	c.Data(200, "text/html; charset=utf-8", page.Bytes())
}

func consolePageCSRF(c *app.RequestContext) string {
	cookie := "ef_console_v2_csrf"
	if v, ok := c.Get("console_session_slot"); ok {
		if slot, ok := v.(int); ok && slot > 0 {
			cookie += "_" + strconv.Itoa(slot)
		}
	}
	return string(c.Cookie(cookie))
}

//go:embed stored_wallet.html
var walletHTML string
var walletPage = template.Must(template.New("wallet").Parse(walletHTML))

func WalletPage(_ context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	var page bytes.Buffer
	if err := walletPage.Execute(&page, consolePageCSRF(c)); err != nil {
		replyError(c, 500, "PAGE_UNAVAILABLE", "无法显示钱包")
		return
	}
	c.Data(200, "text/html; charset=utf-8", page.Bytes())
}
