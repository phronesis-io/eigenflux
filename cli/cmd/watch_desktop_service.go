package cmd

import (
	"cli.eigenflux.ai/internal/client"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/desktopnotify"
)

const desktopAgentNamePlaceholder = "{recipient_agent_name}"

var errDesktopServiceName = errors.New("service_name_unavailable")

// Resolve only in the desktop delivery lane, outside intake and remote ACK.
// The frozen order title is display data; it never selects an Agent or operation.
func (w *accountWatch) resolveDesktopServiceName(ctx context.Context, message desktopnotify.Message) (desktopnotify.Message, error) {
	compact := strings.HasPrefix(strings.TrimPrefix(message.Body, desktopCounterpartyPlaceholder), desktopCardService+"\n")
	if !compact && !strings.Contains(message.Title, desktopServiceNamePlaceholder) && !strings.Contains(message.Body, desktopServiceNamePlaceholder) && !strings.Contains(message.Title, desktopAgentNamePlaceholder) {
		return message, nil
	}
	if desktopnotify.Validate(message) != nil {
		return message, errDesktopServiceName
	}
	target, err := url.Parse(message.URL)
	if err != nil {
		return message, errDesktopServiceName
	}
	orderID, role := target.Query().Get("order_id"), target.Query().Get("role")
	expected, err := orderDesktopURL(w.server.Endpoint, w.identity.AgentID, orderID, role)
	if err != nil || expected != message.URL {
		return message, errDesktopServiceName
	}
	api, order, err := w.readCommissionNoticeOrder(ctx, orderID, role, w.identity.AgentID)
	if err != nil {
		return message, errDesktopServiceName
	}
	name := desktopCleanDisplayText(order.Contract.Title)
	if name == "" {
		return message, errDesktopServiceName
	}
	english := !strings.HasPrefix(message.Title, "「")
	orderStateTitle := ""
	if compact {
		first, _ := utf8.DecodeRuneInString(message.Title)
		english = first < utf8.RuneSelf
	} else {
		roleName := "买方"
		if role == "seller" {
			roleName = "卖方"
		}
		if english {
			roleName = "Buyer"
			if role == "seller" {
				roleName = "Seller"
			}
		}
		// Reorder only trusted copy, before expanding user-provided display text.
		if status, ok := strings.CutPrefix(message.Title, "「"+desktopServiceNamePlaceholder+"」"); ok {
			if status == "已接单，等待买方付款" {
				status = "已接单 · 待付款"
			}
			orderStateTitle = status
			message.Title = status + "｜" + desktopServiceNamePlaceholder
		}
		message.Title = "【" + roleName + " · " + desktopAgentNamePlaceholder + "】" + message.Title
	}
	counterparty := w.desktopCommissionCounterparty(ctx, message.Body, order, role, english)
	if compact {
		accountLine := desktopAgentNamePlaceholder
		if strings.HasPrefix(message.Body, desktopCounterpartyPlaceholder) {
			label := "当前："
			if english {
				label = "Account: "
			}
			accountLine = desktopCounterpartyPlaceholder + " · " + label + accountLine
			message.Body = strings.TrimPrefix(message.Body, desktopCounterpartyPlaceholder)
			counterparty = strings.TrimSuffix(counterparty, "\n")
		}
		message.Body = accountLine + "\n" + message.Body
	}
	agentName := w.commissionPublicAgentName(ctx, w.identity.AgentID, english, false)
	if agentName == "" {
		agentName = "Agent " + w.identity.AgentID
	}
	buyerName := ""
	if !strings.Contains(message.Body, desktopCounterpartyPlaceholder) && strings.Contains(message.Body, "买方") {
		buyerName = w.commissionPublicAgentName(ctx, strconv.FormatInt(int64(order.BuyerID), 10), false, true)
	}
	if buyerName != "" {
		message.Body = strings.ReplaceAll(message.Body, "买方", "买方{order_buyer_agent_name}")
	}
	orderAmount, refundAmount := "订单金额", "退款金额"
	if english {
		orderAmount, refundAmount = "Order amount", "Refund amount"
	}
	var credit *commissionNoticeCredit
	creditText := ""
	if role == "seller" && (strings.Contains(message.Body, desktopCreditPlaceholder) || strings.Contains(message.Body, desktopCardCredit)) {
		credit = readCommissionNoticeCredit(ctx, api, order.ID)
		creditText = formatDesktopCommissionCredit(order, credit, english)
	}
	var orderAmountText, refundAmountText, orderAmountCard, refundAmountCard, net string
	if role == "buyer" {
		orderAmountText = desktopCommissionAmount(order.Contract.Currency, order.Contract.GrossFen, orderAmount)
		refundAmountText = desktopCommissionAmount(order.Contract.Currency, order.Contract.GrossFen, refundAmount)
		if order.Contract.Currency == "CNY" && order.Contract.GrossFen > 0 {
			orderAmountCard = " · " + commissionNoticeCNY(order.Contract.GrossFen)
			refundAmountCard = orderAmountCard
		}
	} else if amount := commissionNoticeNet(order, credit); amount != nil {
		label := "供货款"
		if english {
			label = "Supply payment"
		}
		orderAmountText = desktopCommissionAmount(order.Contract.Currency, *amount, label)
		orderAmountCard = " · " + commissionNoticeCNY(*amount)
		net = " " + commissionNoticeCNY(*amount)
	}
	// A single replacer never expands placeholder-like text within display names.
	replace := strings.NewReplacer(
		desktopServiceNamePlaceholder, name,
		desktopAgentNamePlaceholder, agentName,
		"{order_buyer_agent_name}", "「"+buyerName+"」",
		desktopCounterpartyPlaceholder, counterparty,
		desktopOrderAmountPlaceholder, orderAmountText,
		desktopRefundAmountPlaceholder, refundAmountText,
		desktopCreditPlaceholder, creditText,
		desktopActionDeadlinePlaceholder, desktopCommissionActionDeadline(order, english, compact),
		desktopCardService, name,
		desktopCardGross, orderAmountCard,
		desktopCardRefund, refundAmountCard,
		desktopCardNet, net,
		desktopCardCredit, desktopCardCreditLine(credit, english),
	)
	message.Title = replace.Replace(message.Title)
	message.Body = replace.Replace(message.Body)
	if compact {
		message.Body = strings.TrimSpace(message.Body)
	} else if orderStateTitle != "" && desktopnotify.Validate(message) != nil {
		// Keep the state in the title when the full labels exceed the notifier's title limit.
		message.Title = orderStateTitle
		message.Body = name + " · " + agentName + "\n" + message.Body
	}
	if desktopnotify.Validate(message) != nil {
		return message, errDesktopServiceName
	}
	return message, nil
}

func (w *accountWatch) commissionPublicAgentName(ctx context.Context, agentID string, english, preferAgentName bool) string {
	// Public identity data is optional; a failed lookup must not suppress an order notice.
	public := client.New(strings.TrimRight(w.server.Endpoint, "/")+"/api/v2", "", version, client.Meta{})
	public.HTTPClient = &http.Client{Timeout: 3 * time.Second, Transport: watchTransport{ctx}}
	if result, lookupErr := public.Get("/public/agents/by-id/"+agentID+"/card", nil); lookupErr == nil && result.Code == 0 && len(result.Data) <= 256<<10 {
		var profile struct {
			Card struct {
				Name          string `json:"agent_name"`
				DisplayName   string `json:"display_name"`
				NameEN        string `json:"agent_name_en"`
				DisplayNameEN string `json:"display_name_en"`
			} `json:"card"`
			Identity struct {
				DisplayName   string `json:"display_name"`
				DisplayNameEN string `json:"display_name_en"`
			} `json:"public_identity"`
		}
		if json.Unmarshal(result.Data, &profile) == nil {
			candidates := []string{profile.Identity.DisplayName, profile.Card.DisplayName, profile.Card.Name}
			if preferAgentName {
				candidates = []string{profile.Card.Name, profile.Identity.DisplayName, profile.Card.DisplayName}
			}
			if english {
				candidates = append([]string{profile.Identity.DisplayNameEN, profile.Card.DisplayNameEN, profile.Card.NameEN}, candidates...)
			}
			for _, candidate := range candidates {
				if cleaned := desktopCleanDisplayText(candidate); cleaned != "" {
					return cleaned
				}
			}
		}
	}
	return ""
}

func desktopCleanDisplayText(title string) string {
	if !utf8.ValidString(title) {
		return ""
	}
	title = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, title)
	return strings.Join(strings.Fields(title), " ")
}
