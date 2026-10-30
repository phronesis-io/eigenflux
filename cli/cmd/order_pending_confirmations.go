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

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/desktopnotify"
	"github.com/spf13/cobra"
)

const pendingConfirmationMaxPages = 20

type pendingConfirmation struct {
	OrderID           string `json:"order_id"`
	Version           int64  `json:"version"`
	State             string `json:"state"`
	BuyerConfirmDueAt int64  `json:"buyer_confirm_due_at"`
	URL               string `json:"url"`
}

type pendingConfirmationsResult struct {
	SchemaVersion string                `json:"schema_version"`
	Status        string                `json:"status"`
	AgentID       string                `json:"agent_id"`
	CheckedAt     int64                 `json:"checked_at"`
	Orders        []pendingConfirmation `json:"orders"`
	Reminder      string                `json:"reminder"`
}

type pendingConfirmationAccount struct {
	home, agent, principal, token string
	server                        config.Server
	v2                            bool
}

func newOrderPendingConfirmationsCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "pending-confirmations",
		Short: "Read buyer deliveries awaiting confirmation and prepare a reminder",
		Long:  "Read current buyer orders awaiting confirmation. The JSON result is read-only and may be shown after every regular heartbeat or Feed cycle. This command never confirms delivery, marks reminders read, or calls an Agent.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			language, _ := cmd.Flags().GetString("language")
			if language == "" {
				language = desktopnotify.SystemLanguage()
			}
			if language != "zh" && language != "en" {
				return errors.New("--language must be zh or en")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			account, err := resolvePendingConfirmationAccount()
			if err != nil {
				return err
			}
			api, err := account.client(ctx)
			if err != nil {
				return err
			}
			result, err := readPendingConfirmations(ctx, api, account, language)
			if err != nil {
				return err
			}
			// Do not expose a previously selected account after a concurrent switch.
			return auth.WithV2CredentialsLockContext(ctx, account.server.Name, 5*time.Second, func() error {
				if err := account.check(ctx); err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			})
		},
	}
	command.Flags().String("language", "", "Reminder language: zh or en (default: system language)")
	return command
}

func resolvePendingConfirmationAccount() (pendingConfirmationAccount, error) {
	var account pendingConfirmationAccount
	cfg, err := config.Load()
	if err != nil {
		return account, err
	}
	server, err := cfg.GetActive(serverFlag)
	if err != nil {
		return account, err
	}
	account.home, account.server = config.HomeDir(), *server
	account.v2, err = auth.HasV2Credentials(server.Name)
	if err != nil {
		return account, err
	}
	if account.v2 {
		credentials, err := auth.LoadV2Credentials(server.Name)
		if err != nil {
			return account, err
		}
		account.agent, account.principal, account.token = credentials.AgentID, credentials.PrincipalID, credentials.AccessToken
	} else {
		credentials, err := auth.LoadCredentials(server.Name)
		if err != nil {
			return account, err
		}
		if credentials.IsExpired() {
			return account, errors.New("buyer authentication has expired")
		}
		account.agent, account.token = credentials.AgentID, credentials.AccessToken
	}
	if id, err := strconv.ParseInt(account.agent, 10, 64); err != nil || id <= 0 || strconv.FormatInt(id, 10) != account.agent || account.token == "" {
		return account, errors.New("buyer account identity is unavailable")
	}
	return account, nil
}

func (account pendingConfirmationAccount) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if config.HomeDir() != account.home {
		return errWatchIdentity
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	server, err := cfg.GetActive(serverFlag)
	if err != nil || server.Name != account.server.Name || server.Endpoint != account.server.Endpoint || server.CommissionEndpoint != account.server.CommissionEndpoint {
		return errWatchConfiguration
	}
	v2, err := auth.HasV2Credentials(account.server.Name)
	if err != nil || v2 != account.v2 {
		return errWatchIdentity
	}
	if account.v2 {
		credentials, err := auth.LoadV2Credentials(account.server.Name)
		if err != nil || credentials.AgentID != account.agent || credentials.PrincipalID != account.principal {
			return errWatchIdentity
		}
	} else {
		credentials, err := auth.LoadCredentials(account.server.Name)
		if err != nil || credentials.AgentID != account.agent || credentials.AccessToken != account.token {
			return errWatchIdentity
		}
	}
	return nil
}

func (account pendingConfirmationAccount) client(ctx context.Context) (*client.Client, error) {
	if err := account.check(ctx); err != nil {
		return nil, err
	}
	base, err := account.server.CommissionBaseURL()
	if err != nil {
		return nil, err
	}
	api := client.New(strings.TrimRight(base, "/")+"/api/v1", account.token, version, clientMetaForServer(&account.server))
	api.HTTPClient = &http.Client{Timeout: 10 * time.Second, Transport: watchTransport{ctx}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if account.v2 {
		api.OnUnauthorized = func() (string, error) {
			if err := account.check(ctx); err != nil {
				return "", err
			}
			credentials, err := refreshPinnedV2Credentials(ctx, account.server.Name, account.server.Endpoint, true, account.agent, account.principal)
			if err != nil {
				return "", err
			}
			return credentials.AccessToken, account.check(ctx)
		}
	}
	return api, nil
}

func readPendingConfirmations(ctx context.Context, api *client.Client, account pendingConfirmationAccount, language string) (pendingConfirmationsResult, error) {
	result := pendingConfirmationsResult{SchemaVersion: "eigenflux_pending_confirmations.v1", Status: "empty", AgentID: account.agent, Orders: []pendingConfirmation{}}
	seenCursors, seenOrders := map[string]bool{"": true}, map[string]bool{}
	cursor := ""
	for page := 0; page < pendingConfirmationMaxPages; page++ {
		if err := account.check(ctx); err != nil {
			return pendingConfirmationsResult{}, err
		}
		params := map[string]string{"role": "buyer", "state": "awaiting_buyer_confirmation", "limit": "100"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		response, requestErr := api.Get("/orders", params)
		if err := account.check(ctx); err != nil {
			return pendingConfirmationsResult{}, err
		}
		if requestErr != nil {
			var denied *client.APIError
			if errors.As(requestErr, &denied) && denied.StatusCode == http.StatusForbidden && denied.Code == http.StatusForbidden && denied.ErrorCode == "" && denied.Msg == "commission access is not allowed" {
				return pendingConfirmationsResult{SchemaVersion: result.SchemaVersion, Status: "not_enabled", AgentID: account.agent, CheckedAt: time.Now().UnixMilli(), Orders: []pendingConfirmation{}}, nil
			}
			return pendingConfirmationsResult{}, fmt.Errorf("pending confirmation lookup failed: %w", requestErr)
		}
		var data struct {
			Orders     json.RawMessage `json:"orders"`
			NextCursor *string         `json:"next_cursor"`
		}
		if response.Code != 0 || json.Unmarshal(response.Data, &data) != nil || len(data.Orders) == 0 || data.NextCursor == nil {
			return pendingConfirmationsResult{}, errors.New("invalid pending confirmation page")
		}
		var orders []struct {
			ID                notificationID `json:"order_id"`
			Buyer             notificationID `json:"buyer_agent_id"`
			Version           int64          `json:"version"`
			State             string         `json:"state"`
			BuyerConfirmDueAt int64          `json:"buyer_confirm_due_at"`
		}
		if json.Unmarshal(data.Orders, &orders) != nil || len(orders) > 100 {
			return pendingConfirmationsResult{}, errors.New("invalid pending confirmation orders")
		}
		for _, order := range orders {
			id := strconv.FormatInt(int64(order.ID), 10)
			if order.ID <= 0 || strconv.FormatInt(int64(order.Buyer), 10) != account.agent || order.Version <= 0 || order.State != "awaiting_buyer_confirmation" || order.BuyerConfirmDueAt < 0 || seenOrders[id] {
				return pendingConfirmationsResult{}, errors.New("pending confirmation order identity or state mismatch")
			}
			target, err := orderDesktopURL(account.server.Endpoint, account.agent, id, "buyer")
			if err != nil {
				return pendingConfirmationsResult{}, err
			}
			seenOrders[id] = true
			result.Orders = append(result.Orders, pendingConfirmation{OrderID: id, Version: order.Version, State: order.State, BuyerConfirmDueAt: order.BuyerConfirmDueAt, URL: target})
		}
		cursor = *data.NextCursor
		if cursor == "" {
			result.CheckedAt = time.Now().UnixMilli()
			if len(result.Orders) > 0 {
				result.Status = "pending"
				result.Reminder = fmt.Sprintf("You have %d delivered order(s) awaiting confirmation. Please review the results and confirm your orders promptly.", len(result.Orders))
				if language == "zh" {
					result.Reminder = fmt.Sprintf("您有 %d 笔订单已交付，等待确认。请及时检查交付结果并确认订单。", len(result.Orders))
				}
			}
			return result, nil
		}
		if len(cursor) > 4096 || seenCursors[cursor] || len(orders) == 0 {
			return pendingConfirmationsResult{}, errors.New("invalid or repeated pending confirmation cursor")
		}
		seenCursors[cursor] = true
	}
	return pendingConfirmationsResult{}, errors.New("pending confirmation page limit exceeded; result is incomplete")
}

func init() { orderCmd.AddCommand(newOrderPendingConfirmationsCommand()) }
