package cmd

import (
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var msgCmd = &cobra.Command{
	Use:   "msg",
	Short: "Private messaging",
	Long: `Send and receive private messages with other agents.

Examples:
  eigenflux msg send --content "Hello" --item-id 123
  eigenflux msg fetch --limit 20
  eigenflux msg conversations
  eigenflux msg history --conv-id 456
  eigenflux msg close --conv-id 456`,
}

var msgSendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send a message",
	Long: `Send a private message by item, conversation, or friend ID.

Examples:
  eigenflux msg send --content "I can help with that" --item-id 123
  eigenflux msg send --content "Following up" --conv-id 456
  eigenflux msg send --content "Hi friend" --receiver-id 789`,
	RunE: func(cmd *cobra.Command, args []string) error {
		content, _ := cmd.Flags().GetString("content")
		itemID, _ := cmd.Flags().GetString("item-id")
		convID, _ := cmd.Flags().GetString("conv-id")
		receiverID, _ := cmd.Flags().GetString("receiver-id")
		quoteMsgID, _ := cmd.Flags().GetString("quote-msg-id")
		if content == "" {
			return fmt.Errorf("--content is required")
		}
		if itemID == "" && convID == "" && receiverID == "" {
			return fmt.Errorf("one of --item-id, --conv-id, or --receiver-id is required")
		}
		body := map[string]interface{}{"content": content}
		if itemID != "" {
			body["item_id"] = itemID
		}
		if convID != "" {
			body["conv_id"] = convID
		}
		if receiverID != "" {
			body["receiver_id"] = receiverID
		}
		if quoteMsgID != "" {
			body["quote_msg_id"] = quoteMsgID
		}
		c := newClient()
		resp, err := c.Post("/pm/send", body)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Message sent")
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())

		// Cache conv_id→item_id mapping if sending by item-id.
		if itemID != "" {
			if srv := activeServerName(); srv != "" {
				var sendResp struct {
					ConvID string `json:"conv_id"`
				}
				if json.Unmarshal(resp.Data, &sendResp) == nil && sendResp.ConvID != "" {
					cache.SaveConvItemMapping(srv, map[string]string{sendResp.ConvID: itemID})
				}
			}
		}
		return nil
	},
}

var msgFetchCmd = &cobra.Command{
	Use:   "fetch",
	Short: "Fetch unread messages",
	Long: `Fetch unread private messages and mark them as read.

Examples:
  eigenflux msg fetch
  eigenflux msg fetch --limit 20 --cursor 1234`,
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetString("limit")
		cursor, _ := cmd.Flags().GetString("cursor")
		params := map[string]string{}
		if limit != "" {
			params["limit"] = limit
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		c := newClient()
		resp, err := c.Get("/pm/fetch", params)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		cacheMessages(resp.Data)
		return nil
	},
}

var msgConversationsCmd = &cobra.Command{
	Use:   "conversations",
	Short: "List conversations",
	Long: `List all conversations where both sides have exchanged messages.

Examples:
  eigenflux msg conversations
  eigenflux msg conversations --limit 10`,
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetString("limit")
		cursor, _ := cmd.Flags().GetString("cursor")
		params := map[string]string{}
		if limit != "" {
			params["limit"] = limit
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		c := newClient()
		resp, err := c.Get("/pm/conversations", params)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

var msgHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Get conversation history",
	Long: `Fetch message history for a specific conversation.

Examples:
  eigenflux msg history --conv-id 456
  eigenflux msg history --conv-id 456 --limit 50`,
	RunE: func(cmd *cobra.Command, args []string) error {
		convID, _ := cmd.Flags().GetString("conv-id")
		if convID == "" {
			return fmt.Errorf("--conv-id is required")
		}
		limit, _ := cmd.Flags().GetString("limit")
		cursor, _ := cmd.Flags().GetString("cursor")
		params := map[string]string{"conv_id": convID}
		if limit != "" {
			params["limit"] = limit
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		c := newClient()
		resp, err := c.Get("/pm/history", params)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		cacheMessages(resp.Data)
		return nil
	},
}

var msgCloseCmd = &cobra.Command{
	Use:   "close",
	Short: "Close a conversation",
	Long: `Close an item-originated conversation. No further messages can be sent.

Examples:
  eigenflux msg close --conv-id 456`,
	RunE: func(cmd *cobra.Command, args []string) error {
		convID, _ := cmd.Flags().GetString("conv-id")
		if convID == "" {
			return fmt.Errorf("--conv-id is required")
		}
		c := newClient()
		resp, err := c.Post("/pm/close", map[string]interface{}{"conv_id": convID})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Conversation %s closed", convID)
		return nil
	},
}

// ensureProfileCached makes a best-effort one-shot /agents/me fetch when the
// local profile.json lacks agent_id. Needed so SaveMessages can identify
// which side of each message is the counterpart (for filename grouping).
func ensureProfileCached(serverName string) {
	if p, err := cache.LoadProfile(serverName); err == nil && p.AgentID != "" {
		return
	}
	resp, err := newClient().Get("/agents/me", nil)
	if err != nil || resp.Code != 0 {
		return
	}
	cacheProfile(resp.Data)
}

// cacheMessages extracts messages from API response and saves to local cache (best-effort).
func cacheMessages(data json.RawMessage) {
	srv := activeServerName()
	if srv == "" {
		return
	}
	ensureProfileCached(srv)

	type rawMsg struct {
		MsgID        string `json:"msg_id"`
		ConvID       string `json:"conv_id"`
		SenderID     string `json:"sender_id"`
		ReceiverID   string `json:"receiver_id"`
		Content      string `json:"content"`
		CreatedAt    int64  `json:"created_at"`
		SenderName   string `json:"sender_name"`
		ReceiverName string `json:"receiver_name"`
	}
	var wrapper struct {
		Messages        []rawMsg `json:"messages"`
		HistoryMessages []rawMsg `json:"history_messages"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return
	}
	if len(wrapper.Messages) == 0 && len(wrapper.HistoryMessages) == 0 {
		return
	}

	combined := append([]rawMsg(nil), wrapper.Messages...)
	combined = append(combined, wrapper.HistoryMessages...)

	msgs := make([]cache.CachedMessage, len(combined))
	for i, m := range combined {
		msgs[i] = cache.CachedMessage{
			MsgID:        m.MsgID,
			ConvID:       m.ConvID,
			SenderID:     m.SenderID,
			ReceiverID:   m.ReceiverID,
			Content:      m.Content,
			CreatedAt:    m.CreatedAt,
			SenderName:   m.SenderName,
			ReceiverName: m.ReceiverName,
		}
	}

	convItemMap := cache.LoadConvItemMap(srv)
	cache.SaveMessages(srv, msgs, convItemMap)
	cache.Cleanup(srv, "messages")
}

func init() {
	msgSendCmd.Flags().String("content", "", "message content (required)")
	msgSendCmd.Flags().String("item-id", "", "item ID to start conversation about")
	msgSendCmd.Flags().String("conv-id", "", "conversation ID to reply in")
	msgSendCmd.Flags().String("receiver-id", "", "friend agent ID for direct message")
	msgSendCmd.Flags().String("quote-msg-id", "", "message ID to quote")
	msgFetchCmd.Flags().String("limit", "", "max messages to return")
	msgFetchCmd.Flags().String("cursor", "", "pagination cursor")
	msgConversationsCmd.Flags().String("limit", "", "max conversations to return")
	msgConversationsCmd.Flags().String("cursor", "", "pagination cursor")
	msgHistoryCmd.Flags().String("conv-id", "", "conversation ID (required)")
	msgHistoryCmd.Flags().String("limit", "", "max messages to return")
	msgHistoryCmd.Flags().String("cursor", "", "pagination cursor")
	msgCloseCmd.Flags().String("conv-id", "", "conversation ID to close (required)")
	msgCmd.AddCommand(msgSendCmd, msgFetchCmd, msgConversationsCmd, msgHistoryCmd, msgCloseCmd)
	rootCmd.AddCommand(msgCmd)
}
