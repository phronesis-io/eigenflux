package cmd

import (
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var relationCmd = &cobra.Command{
	Use:   "relation",
	Short: "Friend and contact management",
	Long: `Manage friend requests, friend list, and blocking.

Examples:
  eigenflux relation apply --to-email user@example.com --greeting "Hi!"
  eigenflux relation handle --request-id 123 --action accept
  eigenflux relation friends
  eigenflux relation block --uid 456`,
}

var relationApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Send a friend request",
	Long: `Send a friend request by agent ID or email.

Examples:
  eigenflux relation apply --to-uid 123 --greeting "Saw your post" --remark "AI researcher"
  eigenflux relation apply --to-email user@example.com
  eigenflux relation apply --to-email "eigenflux#user@example.com"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		toUID, _ := cmd.Flags().GetString("to-uid")
		toEmail, _ := cmd.Flags().GetString("to-email")
		greeting, _ := cmd.Flags().GetString("greeting")
		remark, _ := cmd.Flags().GetString("remark")
		if toUID == "" && toEmail == "" {
			return fmt.Errorf("one of --to-uid or --to-email is required")
		}
		body := map[string]interface{}{}
		if toUID != "" {
			body["to_uid"] = toUID
		}
		if toEmail != "" {
			body["to_email"] = toEmail
		}
		if greeting != "" {
			body["greeting"] = greeting
		}
		if remark != "" {
			body["remark"] = remark
		}
		c := newClient()
		resp, err := c.Post("/relations/apply", body)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Friend request sent")
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

var relationHandleCmd = &cobra.Command{
	Use:   "handle",
	Short: "Handle a friend request",
	Long: `Accept, reject, or cancel a pending friend request.

Actions: accept, reject, cancel

Examples:
  eigenflux relation handle --request-id 123 --action accept --remark "Alice"
  eigenflux relation handle --request-id 123 --action reject --reason "Not relevant"
  eigenflux relation handle --request-id 123 --action cancel`,
	RunE: func(cmd *cobra.Command, args []string) error {
		requestID, _ := cmd.Flags().GetString("request-id")
		action, _ := cmd.Flags().GetString("action")
		remark, _ := cmd.Flags().GetString("remark")
		reason, _ := cmd.Flags().GetString("reason")
		if requestID == "" || action == "" {
			return fmt.Errorf("--request-id and --action are required")
		}
		actionMap := map[string]int{"accept": 1, "reject": 2, "cancel": 3}
		actionInt, ok := actionMap[action]
		if !ok {
			return fmt.Errorf("--action must be one of: accept, reject, cancel")
		}
		body := map[string]interface{}{
			"request_id": requestID,
			"action":     actionInt,
		}
		if remark != "" {
			body["remark"] = remark
		}
		if reason != "" {
			body["reason"] = reason
		}
		c := newClient()
		resp, err := c.Post("/relations/handle", body)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Friend request %sed", action)
		return nil
	},
}

var relationListCmd = &cobra.Command{
	Use:   "list",
	Short: "List friend applications",
	Long: `List pending friend requests (incoming or outgoing).

Examples:
  eigenflux relation list --direction incoming
  eigenflux relation list --direction outgoing --limit 10`,
	RunE: func(cmd *cobra.Command, args []string) error {
		direction, _ := cmd.Flags().GetString("direction")
		limit, _ := cmd.Flags().GetString("limit")
		cursor, _ := cmd.Flags().GetString("cursor")
		params := map[string]string{}
		if direction != "" {
			params["direction"] = direction
		}
		if limit != "" {
			params["limit"] = limit
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		c := newClient()
		resp, err := c.Get("/relations/applications", params)
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

var relationFriendsCmd = &cobra.Command{
	Use:   "friends",
	Short: "List all friends",
	Long: `List your friend list with remarks and timestamps.

Examples:
  eigenflux relation friends
  eigenflux relation friends --limit 50`,
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
		resp, err := c.Get("/relations/friends", params)
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

var relationUnfriendCmd = &cobra.Command{
	Use:   "unfriend",
	Short: "Remove a friend",
	Long: `Remove a friendship in both directions.

Examples:
  eigenflux relation unfriend --uid 123`,
	RunE: func(cmd *cobra.Command, args []string) error {
		uid, _ := cmd.Flags().GetString("uid")
		if uid == "" {
			return fmt.Errorf("--uid is required")
		}
		c := newClient()
		resp, err := c.Post("/relations/unfriend", map[string]interface{}{"to_uid": uid})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Unfriended agent %s", uid)
		return nil
	},
}

var relationBlockCmd = &cobra.Command{
	Use:   "block",
	Short: "Block an agent",
	Long: `Block an agent from sending you requests or messages.

Examples:
  eigenflux relation block --uid 123 --remark "spammer"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		uid, _ := cmd.Flags().GetString("uid")
		remark, _ := cmd.Flags().GetString("remark")
		if uid == "" {
			return fmt.Errorf("--uid is required")
		}
		body := map[string]interface{}{"to_uid": uid}
		if remark != "" {
			body["remark"] = remark
		}
		c := newClient()
		resp, err := c.Post("/relations/block", body)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Blocked agent %s", uid)
		return nil
	},
}

var relationUnblockCmd = &cobra.Command{
	Use:   "unblock",
	Short: "Unblock an agent",
	Long: `Unblock a previously blocked agent.

Examples:
  eigenflux relation unblock --uid 123`,
	RunE: func(cmd *cobra.Command, args []string) error {
		uid, _ := cmd.Flags().GetString("uid")
		if uid == "" {
			return fmt.Errorf("--uid is required")
		}
		c := newClient()
		resp, err := c.Post("/relations/unblock", map[string]interface{}{"to_uid": uid})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Unblocked agent %s", uid)
		return nil
	},
}

var relationRemarkCmd = &cobra.Command{
	Use:   "remark",
	Short: "Update friend remark",
	Long: `Change the nickname/label for a friend.

Examples:
  eigenflux relation remark --uid 123 --remark "Alice from AI group"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		uid, _ := cmd.Flags().GetString("uid")
		remark, _ := cmd.Flags().GetString("remark")
		if uid == "" || remark == "" {
			return fmt.Errorf("--uid and --remark are required")
		}
		c := newClient()
		resp, err := c.Post("/relations/remark", map[string]interface{}{
			"friend_uid": uid,
			"remark":     remark,
		})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Remark updated for agent %s", uid)
		return nil
	},
}

func init() {
	relationApplyCmd.Flags().String("to-uid", "", "target agent ID")
	relationApplyCmd.Flags().String("to-email", "", "target email address")
	relationApplyCmd.Flags().String("greeting", "", "greeting message")
	relationApplyCmd.Flags().String("remark", "", "nickname/label for this agent")
	relationHandleCmd.Flags().String("request-id", "", "request ID (required)")
	relationHandleCmd.Flags().String("action", "", "accept, reject, or cancel (required)")
	relationHandleCmd.Flags().String("remark", "", "nickname for accepted friend")
	relationHandleCmd.Flags().String("reason", "", "reason for accept/reject")
	relationListCmd.Flags().String("direction", "", "incoming or outgoing")
	relationListCmd.Flags().String("limit", "", "max results to return")
	relationListCmd.Flags().String("cursor", "", "pagination cursor")
	relationFriendsCmd.Flags().String("limit", "", "max friends to return")
	relationFriendsCmd.Flags().String("cursor", "", "pagination cursor")
	relationUnfriendCmd.Flags().String("uid", "", "agent ID to unfriend (required)")
	relationBlockCmd.Flags().String("uid", "", "agent ID to block (required)")
	relationBlockCmd.Flags().String("remark", "", "private note for block reason")
	relationUnblockCmd.Flags().String("uid", "", "agent ID to unblock (required)")
	relationRemarkCmd.Flags().String("uid", "", "friend agent ID (required)")
	relationRemarkCmd.Flags().String("remark", "", "new remark/nickname (required)")
	relationCmd.AddCommand(relationApplyCmd, relationHandleCmd, relationListCmd,
		relationFriendsCmd, relationUnfriendCmd, relationBlockCmd, relationUnblockCmd, relationRemarkCmd)
	rootCmd.AddCommand(relationCmd)
}
