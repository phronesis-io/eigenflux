package tradebff

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"golang.org/x/text/language"
	"html/template"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
)

type walletPageData struct {
	Lang, CSRF, Order, Title, Price, Amount string
}

func (d walletPageData) T(zh, en string) string {
	if d.Lang == "en" {
		return en
	}
	return zh
}

var pageLanguages = language.NewMatcher([]language.Tag{language.SimplifiedChinese, language.English})

func newWalletPageData(c *app.RequestContext) walletPageData {
	preferred := string(c.GetHeader("Accept-Language"))
	if q := c.Query("lang"); q == "zh" || q == "en" {
		preferred = q
	}
	_, i := language.MatchStrings(pageLanguages, preferred)
	lang := []string{"zh-CN", "en"}[i]
	c.Header("Content-Language", lang)
	c.Header("Vary", "Accept-Language")
	return walletPageData{Lang: lang, CSRF: consolePageCSRF(c)}
}

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
	dataPage := newWalletPageData(c)
	id := c.Param("order_id")
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	if !positiveDecimal(id) {
		replyError(c, 400, "INVALID_ORDER_ID", dataPage.T("订单号无效", "Invalid order ID"))
		return
	}
	actor, ok := agentID(c)
	if !ok {
		replyError(c, 401, "CONSOLE_SESSION_REQUIRED", dataPage.T("请先登录 Console", "Sign in to Console"))
		return
	}
	data, err := s.fetch(ctx, actor, "orders:read", "console.trade.orders.get", "GET", "/api/v2/console/trade/orders/"+id, nil, nil, "", false)
	if err != nil {
		replyError(c, upstreamStatus(err), "COMMISSION_REQUEST_FAILED", dataPage.T("无法读取订单", "Unable to read order"))
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
		replyError(c, 502, "INVALID_ORDER", dataPage.T("订单金额无效", "Invalid order amount"))
		return
	}
	if order.Role != "buyer" || order.State != "pending_payment" {
		replyError(c, 409, "ORDER_NOT_PAYABLE", dataPage.T("此账号当前无法支付该订单", "This account cannot pay this order"))
		return
	}
	var page bytes.Buffer
	dataPage.Order = id
	dataPage.Title = order.Contract.Title
	dataPage.Price = fmt.Sprintf("%d.%02d", order.Contract.Amount/100, order.Contract.Amount%100)
	dataPage.Amount = strconv.FormatInt(order.Contract.Amount, 10)
	err = confirmationPage.Execute(&page, dataPage)
	if err != nil {
		replyError(c, 500, "PAGE_UNAVAILABLE", dataPage.T("无法显示确认页面", "Unable to display confirmation"))
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
	data := newWalletPageData(c)
	if err := walletPage.Execute(&page, data); err != nil {
		replyError(c, 500, "PAGE_UNAVAILABLE", data.T("无法显示钱包", "Unable to display wallet"))
		return
	}
	c.Data(200, "text/html; charset=utf-8", page.Bytes())
}
