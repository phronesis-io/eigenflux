package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
	"cli.eigenflux.ai/internal/watchservice"
	"github.com/spf13/cobra"
)

// Separate ownership from a host execution binding: displaying an order must
// not require an Agent adapter or authorize seller acceptance/fulfillment.
func notificationWatchPath(home, server string) string {
	return filepath.Join(home, "watch", watchstate.Scope(home, server, "", "")+".notifications.json")
}

func (w *accountWatch) notificationIdentity() dispatch.Binding {
	return dispatch.Binding{Version: 1, Home: w.home, Server: w.server.Name, Endpoint: w.server.Endpoint,
		AgentID: w.identity.AgentID, PrincipalID: w.identity.PrincipalID, Scope: w.scope}
}

func readNotificationWatch(home, server string) (dispatch.Binding, error) {
	var b dispatch.Binding
	data, err := os.ReadFile(notificationWatchPath(home, server))
	if err != nil {
		return b, err
	}
	if len(data) > 64<<10 || json.Unmarshal(data, &b) != nil || b.Version != 1 {
		return b, errors.New("invalid notification watch ownership")
	}
	return b, nil
}

func (w *accountWatch) enableNotificationsOnly() error {
	return auth.WithV2CredentialsLockContext(context.Background(), w.server.Name, 35*time.Second, func() error {
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return err
		}
		if !w.identityOK(credentials) {
			return errWatchIdentity
		}
		return w.writeNotificationOwner()
	})
}

func (w *accountWatch) writeNotificationOwner() error {
	if _, err := os.Stat(dispatch.BindingPath(w.home, w.server.Name)); err == nil {
		return errors.New("an execution binding already owns this account; use its watch --dispatch")
	} else if !os.IsNotExist(err) {
		return err
	}
	expected := w.notificationIdentity()
	b, err := readNotificationWatch(w.home, w.server.Name)
	if err == nil && !sameBindingIdentity(b, expected) {
		return errWatchIdentity
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return dispatch.WriteJSON(notificationWatchPath(w.home, w.server.Name), expected)
}

func (w *accountWatch) notificationIdentityOK() error {
	b, err := readNotificationWatch(w.home, w.server.Name)
	if err != nil || !sameBindingIdentity(b, w.notificationIdentity()) {
		return errWatchConfiguration
	}
	credentials, err := auth.LoadV2Credentials(w.server.Name)
	if err != nil {
		return err
	}
	if !w.identityOK(credentials) {
		return errWatchIdentity
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	srv, err := cfg.GetActive(w.server.Name)
	if err != nil || srv.Endpoint != w.server.Endpoint || srv.CommissionEndpoint != w.server.CommissionEndpoint {
		return errWatchConfiguration
	}
	if _, err := os.Stat(dispatch.BindingPath(w.home, w.server.Name)); !os.IsNotExist(err) {
		return errWatchConfiguration
	}
	return nil
}

func newWatchStartCommand() *cobra.Command {
	return &cobra.Command{
		Use: "start", Short: "Install and start this account's Windows notification watch",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			srv, err := cfg.GetActive(serverFlag)
			if err != nil {
				return err
			}
			hasAuth, err := auth.HasV2Credentials(srv.Name)
			if err != nil {
				return err
			}
			if !hasAuth {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": "pending_auth", "action": "run watch start after Agent authentication"})
			}
			expected, err := watchBindingIdentity()
			if err != nil {
				return err
			}
			if _, err := os.Stat(dispatch.BindingPath(expected.Home, expected.Server)); !os.IsNotExist(err) {
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": "host_managed", "action": "verify the existing watch --dispatch task"})
			}
			if previous, err := readNotificationWatch(expected.Home, expected.Server); err == nil {
				if !sameBindingIdentity(previous, expected) {
					return errWatchIdentity
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			cli, err := os.Executable()
			if err != nil {
				return err
			}
			result, err := watchservice.StartNotifications(cmd.Context(), watchservice.NotificationTask{
				CLI: cli, Home: expected.Home, Server: expected.Server, Scope: expected.Scope,
				AgentID: expected.AgentID, PrincipalID: expected.PrincipalID,
			})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
}

func init() { watchCmd.AddCommand(newWatchStartCommand()) }
