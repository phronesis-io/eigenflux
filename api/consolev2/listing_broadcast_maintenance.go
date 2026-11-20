package consolev2

import "errors"

const listingBroadcastPhase = "commission_listing_broadcast"

// ListingBroadcast contains identifiers and fixed outcomes only. It never
// carries a listing's text, broadcast body, settings value, or raw error.
type listingBroadcastObservation struct {
	CommissionID   string `json:"commission_id"`
	NotificationID string `json:"notification_id,omitempty"`
	DraftVersion   int64  `json:"draft_version,omitempty"`
	PublicRevision int64  `json:"public_revision,omitempty"`
	BroadcastID    string `json:"broadcast_id,omitempty"`
	Stage          string `json:"stage"`
	Reason         string `json:"reason,omitempty"`
}

func validateListingBroadcast(e maintenanceEvent) error {
	d := e.ListingBroadcast
	if d == nil || e.Component != "cli" || e.Trigger != "auto" || !maintenanceValue.MatchString(e.FromVersion) ||
		e.Host != "" || e.Mode != "" || e.ToVersion != "" || e.RunningVersion != "" || e.ErrorCode != "" ||
		e.DurationMS != 0 || e.Source != "" || e.Platform != "" || e.ReasonCategory != "" ||
		e.Runtime != nil || e.Commission != nil || e.Notification != nil || e.OuterLoop != nil ||
		!canonicalPositiveNotificationID(d.CommissionID) || !validListingBroadcastResult(*d, e.Result) {
		return errors.New("invalid listing broadcast observation")
	}
	return nil
}

func validListingBroadcastResult(d listingBroadcastObservation, result string) bool {
	switch d.Stage {
	case "queue", "handoff":
		if !canonicalPositiveNotificationID(d.NotificationID) || d.DraftVersion <= 0 || d.PublicRevision != 0 || d.BroadcastID != "" {
			return false
		}
		if d.Stage == "queue" {
			switch result {
			case "enqueued":
				return d.Reason == ""
			case "skipped":
				return maintenanceOneOf(d.Reason, "notifications_only", "binding_missing", "commission_unsubscribed", "main_binding_unavailable", "queue_full", "legacy_stream")
			case "failed":
				return maintenanceOneOf(d.Reason, "queue_open_failed", "queue_save_failed")
			}
			return false
		}
		switch result {
		case "conversation_confirmed":
			return d.Reason == ""
		case "skipped":
			if maintenanceOneOf(d.Reason, "main_notice_transport_unverified", "main_session_mismatch", "execution_unconfirmed") {
				return true
			}
			fallthrough
		case "retry_pending":
			return maintenanceOneOf(d.Reason, "identity_changed", "retry_permission_unavailable", "main_unavailable", "main_busy", "main_not_submitted", "main_unconfirmed", "retry_exhausted")
		}
	case "send":
		if d.PublicRevision <= 0 || d.DraftVersion != 0 || d.NotificationID != "" {
			return false
		}
		switch result {
		case "started":
			return d.BroadcastID == "" && d.Reason == ""
		case "submitted":
			return canonicalPositiveNotificationID(d.BroadcastID) && d.Reason == ""
		case "skipped":
			return d.BroadcastID == "" && maintenanceOneOf(d.Reason, "setting_off", "setting_unavailable", "no_shareable_content", "already_claimed", "claim_unavailable")
		case "failed":
			return d.BroadcastID == "" && maintenanceOneOf(d.Reason, "invalid_input", "rejected")
		case "unconfirmed":
			return d.BroadcastID == "" && maintenanceOneOf(d.Reason, "request_unconfirmed", "missing_broadcast_id")
		}
	}
	return false
}
