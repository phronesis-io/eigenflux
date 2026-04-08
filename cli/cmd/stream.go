package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

const (
	pongWait   = 45 * time.Second
	writeWait  = 10 * time.Second
)

var streamCmd = &cobra.Command{
	Use:   "stream",
	Short: "Stream real-time PM push updates via WebSocket",
	Long: `Connect to the EigenFlux stream service and print incoming private
message push events as newline-delimited JSON to stdout.

The command runs until interrupted (Ctrl-C) or the connection drops.

Examples:
  eigenflux stream
  eigenflux stream --cursor 123456789
  eigenflux stream --format json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cursor, _ := cmd.Flags().GetString("cursor")

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		srv, err := cfg.GetActive(serverFlag)
		if err != nil {
			return err
		}
		creds, err := auth.LoadCredentials(srv.Name)
		if err != nil {
			return fmt.Errorf("not logged in to server %q — run 'eigenflux auth login --email <email>' first", srv.Name)
		}
		if creds.IsExpired() {
			return fmt.Errorf("token expired for server %q — run 'eigenflux auth login --email <email>'", srv.Name)
		}

		wsBase := srv.WSBaseURL()
		if wsBase == "" {
			return fmt.Errorf("cannot determine WebSocket URL for server %q — set --stream-endpoint via 'server update'", srv.Name)
		}

		u, err := url.Parse(wsBase + "/ws/pm")
		if err != nil {
			return fmt.Errorf("invalid stream URL: %w", err)
		}
		q := u.Query()
		q.Set("token", creds.AccessToken)
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		u.RawQuery = q.Encode()

		output.PrintMessage("Connecting to %s ...", wsBase)

		conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			return fmt.Errorf("WebSocket connect failed: %w", err)
		}
		defer conn.Close()

		output.PrintMessage("Connected. Streaming PM updates (Ctrl-C to stop)...")

		// Handle pong to keep the connection alive.
		conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(pongWait))
			return nil
		})

		// Graceful shutdown on interrupt.
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)

		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
						output.PrintMessage("Connection closed normally")
					} else if closeErr, ok := err.(*websocket.CloseError); ok && closeErr.Code == 4002 {
						output.PrintMessage("Connection replaced by another session")
					} else {
						output.PrintMessage("Connection lost: %v", err)
					}
					return
				}

				format := resolveFormat()
				if format == "json" {
					// Pass through raw JSON for pipe-friendly output.
					fmt.Fprintln(os.Stdout, string(msg))
				} else {
					// Table format: pretty-print messages.
					var push struct {
						Type string          `json:"type"`
						Data json.RawMessage `json:"data"`
					}
					if err := json.Unmarshal(msg, &push); err != nil {
						fmt.Fprintln(os.Stdout, string(msg))
						continue
					}
					var data struct {
						Messages []struct {
							MsgID      string `json:"msg_id"`
							ConvID     string `json:"conv_id"`
							SenderName string `json:"sender_name"`
							Content    string `json:"content"`
							CreatedAt  int64  `json:"created_at"`
						} `json:"messages"`
						NextCursor string `json:"next_cursor"`
					}
					if err := json.Unmarshal(push.Data, &data); err != nil {
						fmt.Fprintln(os.Stdout, string(msg))
						continue
					}
					for _, m := range data.Messages {
						ts := time.UnixMilli(m.CreatedAt).Format("15:04:05")
						sender := m.SenderName
						if sender == "" {
							sender = m.MsgID
						}
						fmt.Fprintf(os.Stdout, "[%s] %s: %s\n", ts, sender, m.Content)
					}
				}
			}
		}()

		select {
		case <-done:
			return nil
		case <-interrupt:
			output.PrintMessage("Interrupted, closing connection...")
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			return nil
		}
	},
}

func init() {
	streamCmd.Flags().String("cursor", "", "resume from message cursor (msg_id)")
	rootCmd.AddCommand(streamCmd)
}
