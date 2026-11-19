package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
	"github.com/spf13/cobra"
)

func listingBroadcastRef(value string) (maintenance.ListingBroadcast, bool) {
	id, rev, ok := strings.Cut(value, ":")
	commissionID, idErr := strconv.ParseInt(id, 10, 64)
	revision, revErr := strconv.ParseInt(rev, 10, 64)
	if !ok || idErr != nil || revErr != nil || commissionID <= 0 || revision <= 0 ||
		strconv.FormatInt(commissionID, 10) != id || strconv.FormatInt(revision, 10) != rev {
		return maintenance.ListingBroadcast{}, false
	}
	return maintenance.ListingBroadcast{CommissionID: id, PublicRevision: revision, Stage: "send"}, true
}

func listingBroadcastScope(c *client.Client) maintenance.Scope {
	cfg, err := config.Load()
	if err != nil {
		return maintenance.Scope{}
	}
	server, err := cfg.GetActive(serverFlag)
	if err != nil {
		return maintenance.Scope{}
	}
	credentials, err := auth.LoadV2Credentials(server.Name)
	if err != nil || (c != nil && (c.Token != credentials.AccessToken || c.BaseURL != strings.TrimRight(server.Endpoint, "/")+"/api/v2")) {
		return maintenance.Scope{}
	}
	return maintenance.Scope{Home: config.HomeDir(), Server: server.Name, AgentID: credentials.AgentID}
}

func listingBroadcastEvent(detail maintenance.ListingBroadcast, attempt, result, reason string) maintenance.Event {
	detail.Reason = reason
	event := maintenance.NewEvent(attempt, "cli", "auto", maintenance.ListingBroadcastPhase, result)
	event.FromVersion, event.ListingBroadcast = version, &detail
	return event
}

func observeListingBroadcast(scope maintenance.Scope, detail maintenance.ListingBroadcast, attempt, result, reason string) {
	if scope.AgentID != "" {
		_ = maintenance.Record(scope.ForListingBroadcast(), listingBroadcastEvent(detail, attempt, result, reason))
	}
}

func observeListingPublish(scope maintenance.Scope, detail maintenance.ListingBroadcast, attempt string, response *client.APIResponse, err error) {
	if scope.AgentID == "" {
		return
	}
	result, reason := "unconfirmed", "request_unconfirmed"
	var apiErr *client.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && apiErr.StatusCode != http.StatusRequestTimeout:
		result, reason = "failed", "rejected"
	case err == nil && response != nil && response.Code != 0:
		result, reason = "failed", "rejected"
	case err == nil && response != nil:
		var data struct {
			ItemID string `json:"item_id"`
		}
		result, reason = "unconfirmed", "missing_broadcast_id"
		if json.Unmarshal(response.Data, &data) == nil {
			id, parseErr := strconv.ParseInt(data.ItemID, 10, 64)
			if parseErr == nil && id > 0 && strconv.FormatInt(id, 10) == data.ItemID {
				detail.BroadcastID = data.ItemID
				result, reason = "submitted", ""
			}
		}
	}
	observeListingBroadcast(scope, detail, attempt, result, reason)
}

var commissionObserveBroadcastSkipCmd = &cobra.Command{
	Use:   "observe-broadcast-skip COMMISSION_ID:PUBLIC_REVISION REASON",
	Short: "Record a skipped listing broadcast",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		detail, ok := listingBroadcastRef(args[0])
		if !ok {
			return errors.New("invalid listing broadcast reference")
		}
		event := listingBroadcastEvent(detail, maintenance.NewID(), "skipped", args[1])
		if err := maintenance.Validate(event); err != nil {
			return err
		}
		scope := listingBroadcastScope(nil)
		if scope.AgentID != "" {
			_ = maintenance.Record(scope.ForListingBroadcast(), event)
		}
		return nil
	},
}

func init() {
	commissionCmd.AddCommand(commissionObserveBroadcastSkipCmd)
}

func listingNoticeObservation(notice moderationNotification) maintenance.ListingBroadcast {
	return maintenance.ListingBroadcast{
		CommissionID:   strconv.FormatInt(int64(notice.Payload.CommissionID), 10),
		NotificationID: strconv.FormatInt(int64(notice.ID), 10),
		DraftVersion:   notice.Payload.DraftVersion,
		Stage:          "queue",
	}
}

func listingQueueEvent(detail maintenance.ListingBroadcast, result, reason string) maintenance.Event {
	attempt := commissionDiagnosticHash(detail.NotificationID + ":" + detail.Stage + ":" + reason)
	return listingBroadcastEvent(detail, attempt, result, reason)
}

func listingHandoffEvent(item dispatch.MainNotice, status, code string) maintenance.Event {
	detail := maintenance.ListingBroadcast{
		CommissionID: item.CommissionID, NotificationID: item.ID, DraftVersion: item.DraftVersion, Stage: "handoff",
	}
	if status == "delivered" {
		status, code = "conversation_confirmed", ""
	}
	code = strings.TrimPrefix(code, "main_notice_")
	if code == "unavailable" || code == "target_busy" || code == "not_submitted" || code == "unconfirmed" {
		code = "main_" + strings.TrimPrefix(code, "target_")
	}
	if code == "main_unconfirmed" && item.BridgeError != nil {
		switch item.BridgeError.Code {
		case "main_notice_transport_unverified", "main_session_mismatch":
			code = item.BridgeError.Code
		}
	}
	attempt := commissionDiagnosticHash(item.ID + ":handoff:" + strconv.Itoa(item.Attempts))
	return listingBroadcastEvent(detail, attempt, status, code)
}
