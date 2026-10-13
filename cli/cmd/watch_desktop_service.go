package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/desktopnotify"
)

var errDesktopServiceName = errors.New("service_name_unavailable")

// Resolve only in the desktop delivery lane, outside intake and remote ACK.
// The frozen order title is display data; it never selects an Agent or operation.
func (w *accountWatch) resolveDesktopServiceName(ctx context.Context, message desktopnotify.Message) (desktopnotify.Message, error) {
	if !strings.Contains(message.Title, desktopServiceNamePlaceholder) && !strings.Contains(message.Body, desktopServiceNamePlaceholder) {
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
	// ReplaceAll does not recursively expand placeholder-like text in the title.
	message.Title = strings.ReplaceAll(message.Title, desktopServiceNamePlaceholder, name)
	message.Body = strings.ReplaceAll(message.Body, desktopServiceNamePlaceholder, name)
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
