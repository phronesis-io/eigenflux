package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
	"github.com/spf13/cobra"
)

const moderationNotificationSource = "commission_moderation"

type moderationNotification struct {
	ID      notificationID `json:"notification_id"`
	Source  string         `json:"source_type"`
	Type    string         `json:"type"`
	Payload struct {
		ModerationID        notificationID `json:"moderation_id"`
		CommissionID        notificationID `json:"commission_id"`
		RecipientAgentID    notificationID `json:"recipient_agent_id"`
		DraftVersion        int64          `json:"draft_version"`
		NotificationVersion int64          `json:"notification_version"`
		Status              string         `json:"status"`
		Decision            string         `json:"decision"`
		Flag                string         `json:"flag"`
		Field               string         `json:"field"`
		Reason              string         `json:"reason"`
		OccurredAt          int64          `json:"occurred_at"`
	} `json:"payload"`
}

func parseModerationNotification(raw json.RawMessage, agentID string) (moderationNotification, error) {
	var notice moderationNotification
	if len(raw) > 32<<10 || json.Unmarshal(raw, &notice) != nil || notice.ID <= 0 || notice.Source != moderationNotificationSource || notice.Type != "commission.moderation.changed.v1" || notice.Payload.ModerationID <= 0 || notice.Payload.CommissionID <= 0 || notice.Payload.DraftVersion <= 0 || notice.Payload.NotificationVersion <= 0 || notice.Payload.OccurredAt <= 0 || strconv.FormatInt(int64(notice.Payload.RecipientAgentID), 10) != agentID {
		return notice, errors.New("invalid_moderation_notification")
	}
	if moderationStatusText("en", notice.Payload.Status) == "" {
		return notice, errors.New("invalid_moderation_notification_status")
	}
	return notice, nil
}

func moderationStatusText(language, status string) string {
	labels := map[string][2]string{
		"pending":   {"Queued for review", "已提交审核"},
		"review":    {"Awaiting manual review", "等待人工审核"},
		"reject":    {"Listing rejected", "审核未通过"},
		"pass":      {"Approved; publication pending", "审核通过，等待上架"},
		"published": {"Service published", "服务已上架"},
		"failed":    {"Review or publication failed", "审核或上架失败"},
		"stale":     {"Listing changed; publish again", "服务内容已变更，请重新提交上架"},
	}
	label := labels[status]
	if language == "zh" {
		return label[1]
	}
	return label[0]
}

func moderationNotificationText(language string, notice moderationNotification) (string, string) {
	title := fmt.Sprintf("Service #%d · %s", notice.Payload.CommissionID, moderationStatusText(language, notice.Payload.Status))
	body := fmt.Sprintf("Draft v%d", notice.Payload.DraftVersion)
	if language == "zh" {
		title = fmt.Sprintf("服务 #%d · %s", notice.Payload.CommissionID, moderationStatusText(language, notice.Payload.Status))
		body = fmt.Sprintf("草稿版本 %d", notice.Payload.DraftVersion)
	}
	if notice.Payload.Field != "" {
		body += " · " + notice.Payload.Field
	}
	if notice.Payload.Reason != "" {
		body += "\n" + notice.Payload.Reason
	}
	// Provider guidance is display data; remove controls and bound OS notification text.
	body = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, body)
	if runes := []rune(body); len(runes) > 1900 {
		body = string(runes[:1900]) + "…"
	}
	return title, body
}

func (w *accountWatch) enqueueModerationNotification(notice moderationNotification) (bool, error) {
	fresh, err := saveModerationNoticeWithSink(w.home, w.scope, w.identity.AgentID, notice, func() error {
		if w.desktop == nil {
			return nil
		}
		target, err := url.Parse(w.server.Endpoint)
		if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
			return errors.New("invalid_moderation_notification_origin")
		}
		target.Path, target.RawPath = "/dashboard/trade", ""
		title, body := moderationNotificationText(desktopnotify.SystemLanguage(), notice)
		return w.desktop.Enqueue(desktopnotify.Message{ID: w.scope + ":moderation:" + strconv.FormatInt(int64(notice.ID), 10), Title: title, Body: body, URL: target.String()})
	})
	if err != nil {
		return false, err
	}
	// A completed receipt covers every enabled durable sink. Output is recoverable
	// from watch notices even if the host output fails after the receipt is saved.
	if fresh {
		if err := w.emit("commission_moderation_notification", map[string]any{"notification": notice}); err != nil {
			return false, err
		}
	}
	return true, nil
}

func renderModerationNotification(writer io.Writer, format, language string, notice moderationNotification) error {
	if format == "json" {
		return json.NewEncoder(writer).Encode(map[string]any{"type": "commission_moderation_notification", "notification": notice})
	}
	title, body := moderationNotificationText(language, notice)
	_, err := fmt.Fprintf(writer, "%s\n%s\n", title, body)
	return err
}

// The account credential lock serializes writers. Atomic replacement also lets
// watch notices safely read while a watch is receiving. Retain the newest 100.
const moderationInboxLimit = 100
const moderationReceiptLimit = 100000
const moderationReceiptRetention = 90 * 24 * time.Hour
const moderationInboxMaxBytes = 8 << 20

type moderationInbox struct {
	Version  int                      `json:"version"`
	Scope    string                   `json:"scope"`
	Notices  []moderationNotification `json:"notices"`
	Receipts map[string]int64         `json:"receipts,omitempty"`
}

func moderationInboxPath(home, scope string) string {
	return filepath.Join(home, "watch", "moderation-"+scope+".json")
}
func readModerationInbox(home, scope, agentID string) (moderationInbox, error) {
	inbox := moderationInbox{Version: 1, Scope: scope, Notices: []moderationNotification{}}
	file, err := os.Open(moderationInboxPath(home, scope))
	if errors.Is(err, os.ErrNotExist) {
		return inbox, nil
	}
	if err != nil {
		return inbox, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, moderationInboxMaxBytes+1))
	if err != nil {
		return inbox, err
	}
	inbox = moderationInbox{}
	if len(raw) > moderationInboxMaxBytes || json.Unmarshal(raw, &inbox) != nil || inbox.Version != 1 || inbox.Scope != scope || inbox.Notices == nil || len(inbox.Notices) > moderationInboxLimit {
		return inbox, errors.New("invalid_moderation_inbox")
	}
	if len(inbox.Receipts) > moderationReceiptLimit {
		return inbox, errors.New("invalid_moderation_receipts")
	}
	for id, receivedAt := range inbox.Receipts {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id || receivedAt <= 0 {
			return inbox, errors.New("invalid_moderation_receipts")
		}
	}
	seen := map[notificationID]bool{}
	for _, notice := range inbox.Notices {
		raw, _ := json.Marshal(notice)
		if _, err := parseModerationNotification(raw, agentID); err != nil || seen[notice.ID] {
			return inbox, errors.New("invalid_moderation_inbox")
		}
		seen[notice.ID] = true
	}
	return inbox, nil
}
func saveModerationNotice(home, scope, agentID string, notice moderationNotification) (bool, error) {
	return saveModerationNoticeWithSink(home, scope, agentID, notice, nil)
}
func saveModerationNoticeWithSink(home, scope, agentID string, notice moderationNotification, beforeSave func() error) (bool, error) {
	inbox, err := readModerationInbox(home, scope, agentID)
	if err != nil {
		return false, err
	}
	now := time.Now().Unix()
	// Upgrade pre-receipt inboxes conservatively; original receipt times are unknown.
	if inbox.Receipts == nil {
		inbox.Receipts = map[string]int64{}
		for _, existing := range inbox.Notices {
			inbox.Receipts[strconv.FormatInt(int64(existing.ID), 10)] = now
		}
	}
	for id, receivedAt := range inbox.Receipts {
		if receivedAt <= now-int64(moderationReceiptRetention/time.Second) {
			delete(inbox.Receipts, id)
		}
	}
	id := strconv.FormatInt(int64(notice.ID), 10)
	if _, seen := inbox.Receipts[id]; seen {
		return false, dispatch.WriteJSON(moderationInboxPath(home, scope), inbox)
	}
	// Never discard an unexpired receipt just to make space: retain remote pending.
	if len(inbox.Receipts) >= moderationReceiptLimit {
		return false, errors.New("moderation_receipts_full")
	}
	// Durable desktop enqueue comes before the completed receipt. Its stable ID
	// makes a retry safe if the later inbox write fails.
	if beforeSave != nil {
		if err := beforeSave(); err != nil {
			return false, err
		}
	}
	inbox.Receipts[id] = now
	// An expired receipt may still have a retained payload; replace it on redelivery.
	for i, existing := range inbox.Notices {
		if existing.ID == notice.ID {
			inbox.Notices = append(inbox.Notices[:i], inbox.Notices[i+1:]...)
			break
		}
	}
	inbox.Notices = append(inbox.Notices, notice)
	if len(inbox.Notices) > moderationInboxLimit {
		inbox.Notices = inbox.Notices[len(inbox.Notices)-moderationInboxLimit:]
	}
	if err := dispatch.WriteJSON(moderationInboxPath(home, scope), inbox); err != nil {
		return false, err
	}
	return true, nil
}
func init() {
	watchCmd.AddCommand(&cobra.Command{Use: "notices", Short: "Read the latest 100 listing moderation notices for this account", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		identity, err := watchBindingIdentity()
		if err != nil {
			return err
		}
		inbox, err := readModerationInbox(identity.Home, identity.Scope, identity.AgentID)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"version": inbox.Version, "scope": inbox.Scope, "notices": inbox.Notices})
	}})
}
