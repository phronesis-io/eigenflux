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
const desktopShortServiceNamePlaceholder = "{short_service_name}"

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
	name := desktopServiceName(order.Contract.Title)
	if name == "" {
		return message, errDesktopServiceName
	}
	english := !strings.HasPrefix(message.Title, "「")
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
			message.Title = status + "｜" + desktopShortServiceNamePlaceholder
		}
		message.Title = "【" + roleName + " · " + desktopAgentNamePlaceholder + "】" + message.Title
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
	gross, net := "", ""
	if order.Contract.Currency == "CNY" && order.Contract.GrossFen > 0 {
		gross = " · " + commissionNoticeCNY(order.Contract.GrossFen)
	}
	if amount := commissionNoticeNet(order, credit); amount != nil {
		net = " " + commissionNoticeCNY(*amount)
	}
	// A single replacer never expands placeholder-like text within display names.
	replace := strings.NewReplacer(
		desktopShortServiceNamePlaceholder, desktopShortServiceName(order.Contract.Title),
		desktopServiceNamePlaceholder, name,
		desktopAgentNamePlaceholder, agentName,
		"{order_buyer_agent_name}", "「"+buyerName+"」",
		desktopCounterpartyPlaceholder, w.desktopCommissionCounterparty(ctx, message.Body, order, role, english, compact),
		desktopOrderAmountPlaceholder, desktopCommissionAmount(order, orderAmount),
		desktopRefundAmountPlaceholder, desktopCommissionAmount(order, refundAmount),
		desktopCreditPlaceholder, creditText,
		desktopActionDeadlinePlaceholder, desktopCommissionActionDeadline(order, english, compact),
		desktopCardService, desktopCardServiceLine(order, agentName),
		desktopCardGross, gross,
		desktopCardRefund, gross,
		desktopCardNet, net,
		desktopCardCredit, desktopCardCreditLine(credit, english),
	)
	message.Title = replace.Replace(message.Title)
	message.Body = replace.Replace(message.Body)
	if compact {
		message.Body = strings.TrimSpace(message.Body)
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
				if cleaned := desktopServiceName(candidate); cleaned != "" {
					runes := []rune(cleaned)
					if len(runes) > 40 {
						cleaned = string(runes[:39]) + "…"
					}
					return cleaned
				}
			}
		}
	}
	return ""
}

func desktopServiceName(title string) string {
	runes := []rune(desktopCleanDisplayText(title))
	if len(runes) > 80 {
		runes = append(runes[:79], '…')
	}
	return string(runes)
}

func desktopShortServiceName(title string) string {
	runes := []rune(desktopCleanDisplayText(title))
	if len(runes) <= 13 {
		return string(runes)
	}
	return string(runes[:6]) + "…" + string(runes[len(runes)-6:])
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
