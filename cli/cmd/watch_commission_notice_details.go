package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/client"
)

const (
	desktopCounterpartyPlaceholder   = "{order_counterparty}"
	desktopOrderAmountPlaceholder    = "{order_amount}"
	desktopRefundAmountPlaceholder   = "{refund_amount}"
	desktopCreditPlaceholder         = "{wallet_credit}"
	desktopActionDeadlinePlaceholder = "{order_action_deadline}"
)

type commissionNoticeOrder struct {
	ID               notificationID `json:"order_id"`
	BuyerID          notificationID `json:"buyer_agent_id"`
	SellerID         notificationID `json:"seller_agent_id"`
	State            string         `json:"state"`
	Version          int64          `json:"version"`
	SellerConfirmDue int64          `json:"seller_confirm_due_at"`
	PaymentDue       int64          `json:"payment_due_at"`
	DeliveryDue      int64          `json:"delivery_due_at"`
	BuyerConfirmDue  int64          `json:"buyer_confirm_due_at"`
	Contract         struct {
		Title        string `json:"title"`
		Currency     string `json:"currency"`
		GrossFen     int64  `json:"gross_amount_fen"`
		SellerNetFen *int64 `json:"seller_net_fen"`
	} `json:"contract"`
}

func (w *accountWatch) readCommissionNoticeOrder(ctx context.Context, id, role, account string) (*client.Client, commissionNoticeOrder, error) {
	var data struct {
		Order commissionNoticeOrder `json:"order"`
	}
	orderID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || orderID <= 0 || strconv.FormatInt(orderID, 10) != id || (role != "buyer" && role != "seller") {
		return nil, data.Order, errors.New("invalid_commission_notice_target")
	}
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return nil, data.Order, err
	}
	response, err := api.Get("/orders/"+id, nil)
	if err != nil || response.Code != 0 || len(response.Data) > 256<<10 || json.Unmarshal(response.Data, &data) != nil {
		return nil, data.Order, errors.New("commission_notice_order_unavailable")
	}
	order := data.Order
	recipient := order.BuyerID
	if role == "seller" {
		recipient = order.SellerID
	}
	if int64(order.ID) != orderID || order.BuyerID <= 0 || order.SellerID <= 0 || strconv.FormatInt(int64(recipient), 10) != account {
		return nil, order, errors.New("commission_notice_order_mismatch")
	}
	return api, order, nil
}

func (w *accountWatch) commissionNoticeCounterparty(ctx context.Context, order commissionNoticeOrder, role string, english bool) string {
	id := order.SellerID
	if role == "seller" {
		id = order.BuyerID
	}
	return w.commissionPublicAgentName(ctx, strconv.FormatInt(int64(id), 10), english, true)
}

type commissionNoticeCredit struct {
	OrderID     notificationID `json:"order_id"`
	AmountFen   int64          `json:"amount_fen"`
	Currency    string         `json:"currency"`
	AvailableAt int64          `json:"available_at,omitempty"`
	State       string         `json:"settlement_state,omitempty"`
}

func readCommissionNoticeCredit(ctx context.Context, api *client.Client, orderID notificationID) *commissionNoticeCredit {
	lookup, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	read := *api
	read.HTTPClient = &http.Client{Timeout: 2 * time.Second, Transport: watchTransport{lookup}}
	read.OnUnauthorized = nil
	// Newly credited orders are near the front. Keep delayed lookups bounded.
	params := map[string]string{"limit": "100"}
	for range 2 {
		response, err := read.Get("/wallet/credits", params)
		var page struct {
			Credits []commissionNoticeCredit `json:"credits"`
			Next    string                   `json:"next_cursor"`
		}
		if err != nil || response.Code != 0 || len(response.Data) > 256<<10 || json.Unmarshal(response.Data, &page) != nil {
			return nil
		}
		for _, credit := range page.Credits {
			if credit.OrderID == orderID && credit.Currency == "CNY" && credit.AmountFen >= 0 {
				return &credit
			}
		}
		if page.Next == "" || page.Next == params["cursor"] {
			return nil
		}
		params["cursor"] = page.Next
	}
	return nil
}

func (w *accountWatch) desktopCommissionCounterparty(ctx context.Context, body string, order commissionNoticeOrder, role string, english bool) string {
	if !strings.Contains(body, desktopCounterpartyPlaceholder) {
		return ""
	}
	name := w.commissionNoticeCounterparty(ctx, order, role, english)
	if english {
		label := "Seller"
		if role == "seller" {
			label = "Buyer"
		}
		if name == "" {
			name = "name unavailable"
		}
		return label + ": " + name + "\n"
	}
	label := "卖家"
	if role == "seller" {
		label = "买家"
	}
	if name == "" {
		name = "名称暂不可用"
	}
	return label + "：" + name + "\n"
}

func desktopCommissionAmount(currency string, amount int64, label string) string {
	if currency != "CNY" || amount <= 0 {
		return ""
	}
	return label + " " + commissionNoticeCNY(amount) + "\n"
}

func commissionNoticeNet(order commissionNoticeOrder, credit *commissionNoticeCredit) *int64 {
	if credit != nil {
		return &credit.AmountFen
	}
	if order.Contract.Currency == "CNY" && order.Contract.SellerNetFen != nil && *order.Contract.SellerNetFen >= 0 {
		return order.Contract.SellerNetFen
	}
	return nil
}

func formatDesktopCommissionCredit(order commissionNoticeOrder, credit *commissionNoticeCredit, english bool) string {
	amount := commissionNoticeNet(order, credit)
	state := ""
	if credit != nil {
		state = credit.State
	}
	line := ""
	if amount != nil && *amount >= 0 {
		if english {
			line = "Supply payment credited " + commissionNoticeCNY(*amount) + "\n"
		} else {
			line = "供货款入账 " + commissionNoticeCNY(*amount) + "\n"
		}
	}
	zh, en := "可提现时间请查看订单详情。", "See the order for withdrawal availability."
	switch state {
	case "unmatured":
		if credit.AvailableAt > 0 {
			at := time.UnixMilli(credit.AvailableAt).Local().Format("2006-01-02 15:04 -07:00")
			zh, en = "结算期至 "+at+"，届时可申请提现。", "Funds mature at "+at+"."
		}
	case "withdrawable":
		zh, en = "该笔收益当前可提现。", "These funds are available for withdrawal."
	case "reserved":
		zh, en = "该笔收益有款项正在提现处理中。", "A withdrawal against these funds is processing."
	case "withdrawn":
		zh, en = "该笔收益已提现。", "These funds have been withdrawn."
	}
	if english {
		return line + en + "\n"
	}
	return line + zh + "\n"
}

func commissionNoticeCNY(fen int64) string {
	return fmt.Sprintf("¥%d.%02d", fen/100, fen%100)
}

func (order commissionNoticeOrder) deadline() int64 {
	switch order.State {
	case "awaiting_seller":
		return order.SellerConfirmDue
	case "pending_payment":
		return order.PaymentDue
	case "in_progress":
		return order.DeliveryDue
	case "awaiting_buyer_confirmation":
		return order.BuyerConfirmDue
	}
	return 0
}

func desktopCommissionActionDeadline(order commissionNoticeOrder, english, compact bool) string {
	if order.State != "awaiting_seller" && order.State != "in_progress" {
		return ""
	}
	due := order.deadline()
	if due <= 0 {
		return ""
	}
	at := desktopDeadline("en", due)
	if compact {
		at = desktopCardTime(due)
	}
	if english {
		return "Deadline " + at + "\n"
	}
	return "截止时间 " + at + "\n"
}
