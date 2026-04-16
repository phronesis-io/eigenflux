package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"sync"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

const (
	pongWait       = 45 * time.Second
	writeWait      = 10 * time.Second
	reconnectMin   = 5 * time.Second
	reconnectMax   = 120 * time.Second
	reconnectMul   = 2.0
)

var streamCmd = &cobra.Command{
	Use:   "stream",
	Short: "Stream real-time PM push updates via WebSocket",
	Long: `Connect to the EigenFlux stream service and print incoming private
message push events as newline-delimited JSON to stdout.

The command runs until interrupted (Ctrl-C). Automatically reconnects
on connection loss with exponential backoff.

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

		// PM save needs our own agent_id in profile.json to pick the
		// counterpart for the file name. Fetch it once up-front so the
		// first push lands in the right file.
		ensureProfileCached(srv.Name)
		myProfile, _ := cache.LoadProfile(srv.Name)
		myAgentID := ""
		if myProfile != nil {
			myAgentID = myProfile.AgentID
		}

		// Graceful shutdown on interrupt.
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)

		// Track latest cursor for reconnect resume.
		var mu sync.Mutex
		lastCursor := cursor

		backoff := reconnectMin

		for {
			mu.Lock()
			curCursor := lastCursor
			mu.Unlock()

			u, err := url.Parse(wsBase + "/ws/pm")
			if err != nil {
				return fmt.Errorf("invalid stream URL: %w", err)
			}
			q := u.Query()
			q.Set("token", creds.AccessToken)
			if curCursor != "" {
				q.Set("cursor", curCursor)
			}
			u.RawQuery = q.Encode()

			output.PrintMessage("Connecting to %s ...", wsBase)

			dialHeaders := http.Header{}
			if version != "" {
				dialHeaders.Set("X-CLI-Ver", version)
			}
			conn, _, dialErr := websocket.DefaultDialer.Dial(u.String(), dialHeaders)
			if dialErr != nil {
				output.PrintMessage("Connect failed: %v, retrying in %s...", dialErr, backoff)
				select {
				case <-time.After(backoff):
					backoff = time.Duration(float64(backoff) * reconnectMul)
					if backoff > reconnectMax {
						backoff = reconnectMax
					}
					continue
				case <-interrupt:
					return nil
				}
			}

			// Connected — reset backoff.
			backoff = reconnectMin
			output.PrintMessage("Connected. Streaming PM updates (Ctrl-C to stop)...")

			conn.SetReadDeadline(time.Now().Add(pongWait))
			conn.SetPingHandler(func(string) error {
				conn.SetReadDeadline(time.Now().Add(pongWait))
				return conn.WriteControl(websocket.PongMessage, nil, time.Now().Add(writeWait))
			})

			done := make(chan struct{})
			shouldReconnect := true
			format := resolveFormat()

			go func() {
				defer close(done)
				firstPacket := true
				for {
					_, msg, err := conn.ReadMessage()
					if err != nil {
						if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
							output.PrintMessage("Connection closed normally")
							shouldReconnect = false
						} else if closeErr, ok := err.(*websocket.CloseError); ok && closeErr.Code == 4002 {
							output.PrintMessage("Connection replaced by another session")
							shouldReconnect = false
						} else {
							output.PrintMessage("Connection lost: %v", err)
						}
						return
					}

					// Update cursor from message for reconnect resume.
					var envelope struct {
						Data struct {
							NextCursor string `json:"next_cursor"`
						} `json:"data"`
					}
					if json.Unmarshal(msg, &envelope) == nil && envelope.Data.NextCursor != "" {
						mu.Lock()
						lastCursor = envelope.Data.NextCursor
						mu.Unlock()
					}
					var push struct {
						Type string          `json:"type"`
						Data json.RawMessage `json:"data"`
					}
					pushOK := json.Unmarshal(msg, &push) == nil
					if pushOK {
						cacheMessages(push.Data)
					}

					if format == "json" {
						fmt.Fprintln(os.Stdout, string(msg))
					} else {
						if !pushOK {
							fmt.Fprintln(os.Stdout, string(msg))
							continue
						}
						var data struct {
							Messages []streamMsg `json:"messages"`
							History  []streamMsg `json:"history_messages"`
						}
						if err := json.Unmarshal(push.Data, &data); err != nil {
							fmt.Fprintln(os.Stdout, string(msg))
							continue
						}

						if firstPacket {
							if len(data.History) > 0 {
								fmt.Fprintf(os.Stdout, "--- recent history (%d messages) ---\n", len(data.History))
								sort.Slice(data.History, func(i, j int) bool {
									return data.History[i].CreatedAt < data.History[j].CreatedAt
								})
								for _, m := range data.History {
									printHistoryLine(m, myAgentID)
								}
							}
							if len(data.Messages) > 0 {
								fmt.Fprintln(os.Stdout, "--- new messages ---")
								for _, m := range data.Messages {
									printNewLine(m)
								}
							}
							firstPacket = false
						} else {
							for _, m := range data.Messages {
								printNewLine(m)
							}
						}
					}
				}
			}()

			select {
			case <-done:
				conn.Close()
				if !shouldReconnect {
					return nil
				}
				output.PrintMessage("Reconnecting in %s...", backoff)
				select {
				case <-time.After(backoff):
					backoff = time.Duration(float64(backoff) * reconnectMul)
					if backoff > reconnectMax {
						backoff = reconnectMax
					}
				case <-interrupt:
					return nil
				}
			case <-interrupt:
				output.PrintMessage("Interrupted, closing connection...")
				conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				select {
				case <-done:
				case <-time.After(time.Second):
				}
				conn.Close()
				return nil
			}
		}
	},
}

func init() {
	streamCmd.Flags().String("cursor", "", "resume from message cursor (msg_id)")
	rootCmd.AddCommand(streamCmd)
}

type streamMsg struct {
	MsgID        string `json:"msg_id"`
	ConvID       string `json:"conv_id"`
	SenderID     string `json:"sender_id"`
	ReceiverID   string `json:"receiver_id"`
	SenderName   string `json:"sender_name"`
	ReceiverName string `json:"receiver_name"`
	Content      string `json:"content"`
	CreatedAt    int64  `json:"created_at"`
}

func printHistoryLine(m streamMsg, myAgentID string) {
	ts := time.UnixMilli(m.CreatedAt).Format("15:04:05")
	if m.SenderID == myAgentID {
		peer := m.ReceiverName
		if peer == "" {
			peer = m.ReceiverID
		}
		fmt.Fprintf(os.Stdout, "[%s] → %s: %s\n", ts, peer, m.Content)
	} else {
		peer := m.SenderName
		if peer == "" {
			peer = m.SenderID
		}
		fmt.Fprintf(os.Stdout, "[%s] ← %s: %s\n", ts, peer, m.Content)
	}
}

func printNewLine(m streamMsg) {
	ts := time.UnixMilli(m.CreatedAt).Format("15:04:05")
	sender := m.SenderName
	if sender == "" {
		sender = m.SenderID
	}
	fmt.Fprintf(os.Stdout, "[%s] %s: %s\n", ts, sender, m.Content)
}
