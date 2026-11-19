package maintenance

import (
	"errors"
	"os"
)

const ListingBroadcastPhase = "commission_listing_broadcast"

// ListingBroadcast contains identifiers and fixed outcomes only. It never
// carries a listing's text, broadcast body, settings value, or raw error.
type ListingBroadcast struct {
	CommissionID   string `json:"commission_id"`
	NotificationID string `json:"notification_id,omitempty"`
	DraftVersion   int64  `json:"draft_version,omitempty"`
	PublicRevision int64  `json:"public_revision,omitempty"`
	BroadcastID    string `json:"broadcast_id,omitempty"`
	Stage          string `json:"stage"`
	Reason         string `json:"reason,omitempty"`
}

func validateListingBroadcast(e Event) error {
	d := e.ListingBroadcast
	if d == nil || e.Component != "cli" || e.Trigger != "auto" || !safeValue.MatchString(e.FromVersion) ||
		e.Host != "" || e.Mode != "" || e.ToVersion != "" || e.RunningVersion != "" || e.ErrorCode != "" ||
		e.DurationMS != 0 || e.Source != "" || e.Platform != "" || e.ReasonCategory != "" ||
		e.Runtime != nil || e.Commission != nil || e.Notification != nil || e.OuterLoop != nil ||
		!canonicalPositiveNotificationID(d.CommissionID) || !validListingBroadcastResult(*d, e.Result) {
		return errors.New("invalid listing broadcast observation")
	}
	return nil
}

func validListingBroadcastResult(d ListingBroadcast, result string) bool {
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
				return contains([]string{"notifications_only", "binding_missing", "commission_unsubscribed", "main_binding_unavailable", "queue_full", "legacy_stream"}, d.Reason)
			case "failed":
				return contains([]string{"queue_open_failed", "queue_save_failed"}, d.Reason)
			}
			return false
		}
		switch result {
		case "conversation_confirmed":
			return d.Reason == ""
		case "skipped":
			if contains([]string{"main_notice_transport_unverified", "main_session_mismatch", "execution_unconfirmed"}, d.Reason) {
				return true
			}
			fallthrough
		case "retry_pending":
			return contains([]string{"identity_changed", "retry_permission_unavailable", "main_unavailable", "main_busy", "main_not_submitted", "main_unconfirmed", "retry_exhausted"}, d.Reason)
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
			return d.BroadcastID == "" && contains([]string{"setting_off", "setting_unavailable", "no_shareable_content", "already_claimed", "claim_unavailable"}, d.Reason)
		case "failed":
			return d.BroadcastID == "" && contains([]string{"invalid_input", "rejected"}, d.Reason)
		case "unconfirmed":
			return d.BroadcastID == "" && contains([]string{"request_unconfirmed", "missing_broadcast_id"}, d.Reason)
		}
	}
	return false
}

func (s Scope) ForListingBroadcast() Scope {
	s.listingBroadcast = true
	return s
}

func HasListingBroadcastJournal(s Scope) bool {
	path, err := s.ForListingBroadcast().path()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// RecordListingBroadcasts persists a page of diagnostics after its ACK path.
// It shares no lock with the normal journal and does not wait for a busy writer.
func RecordListingBroadcasts(scope Scope, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	for _, event := range events {
		if event.Phase != ListingBroadcastPhase {
			return errors.New("expected listing broadcast observation")
		}
		if err := Validate(event); err != nil {
			return err
		}
	}
	return update(scope.ForListingBroadcast(), func(st *State) error {
		seen := make(map[string]bool, len(st.Events))
		for _, event := range st.Events {
			seen[event.EventID] = true
		}
		for _, event := range events {
			if !seen[event.EventID] {
				st.Events = append(st.Events, event)
				seen[event.EventID] = true
			}
		}
		return nil
	})
}
