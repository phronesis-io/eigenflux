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
	if !strings.Contains(message.Title, desktopServiceNamePlaceholder) && !strings.Contains(message.Body, desktopServiceNamePlaceholder) && !strings.Contains(message.Title, desktopAgentNamePlaceholder) {
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
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return message, errDesktopServiceName
	}
	response, err := api.Get("/orders/"+orderID, nil)
	if err != nil || response.Code != 0 || len(response.Data) > 256<<10 {
		return message, errDesktopServiceName
	}
	var data struct {
		Order struct {
			ID       notificationID `json:"order_id"`
			BuyerID  notificationID `json:"buyer_agent_id"`
			SellerID notificationID `json:"seller_agent_id"`
			Contract struct {
				Title string `json:"title"`
			} `json:"contract"`
		} `json:"order"`
	}
	if json.Unmarshal(response.Data, &data) != nil || data.Order.BuyerID <= 0 || data.Order.SellerID <= 0 || strconv.FormatInt(int64(data.Order.ID), 10) != orderID {
		return message, errDesktopServiceName
	}
	accountID := data.Order.BuyerID
	if role == "seller" {
		accountID = data.Order.SellerID
	}
	if strconv.FormatInt(int64(accountID), 10) != w.identity.AgentID {
		return message, errDesktopServiceName
	}
	name := desktopServiceName(data.Order.Contract.Title)
	if name == "" {
		return message, errDesktopServiceName
	}
	english := !strings.HasPrefix(message.Title, "「")
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
	message.Title = "【" + roleName + " · " + desktopAgentNamePlaceholder + "】" + message.Title
	agentName := "Agent " + w.identity.AgentID
	{
		agentID := w.identity.AgentID
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
				if english {
					candidates = append([]string{profile.Identity.DisplayNameEN, profile.Card.DisplayNameEN, profile.Card.NameEN}, candidates...)
				}
				for _, candidate := range candidates {
					if cleaned := desktopServiceName(candidate); cleaned != "" {
						runes := []rune(cleaned)
						if len(runes) > 40 {
							cleaned = string(runes[:39]) + "…"
						}
						agentName = cleaned
						break
					}
				}
			}
		}
	}
	// A single replacer never expands placeholder-like text within display names.
	replace := strings.NewReplacer(desktopServiceNamePlaceholder, name, desktopAgentNamePlaceholder, agentName)
	message.Title = replace.Replace(message.Title)
	message.Body = replace.Replace(message.Body)
	if desktopnotify.Validate(message) != nil {
		return message, errDesktopServiceName
	}
	return message, nil
}

func desktopServiceName(title string) string {
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
	runes := []rune(strings.Join(strings.Fields(title), " "))
	if len(runes) > 80 {
		runes = append(runes[:79], '…')
	}
	return string(runes)
}
