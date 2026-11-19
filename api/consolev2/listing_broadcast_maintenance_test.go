package consolev2

import (
	"strings"
	"testing"
	"time"
)

func listingBroadcastTestEvent() maintenanceEvent {
	return maintenanceEvent{
		EventID: strings.Repeat("e", 64), AttemptID: strings.Repeat("b", 32), EventAt: time.Now().UnixMilli(),
		Component: "cli", Trigger: "auto", FromVersion: "1.0.43", Phase: listingBroadcastPhase, Result: "submitted",
		ListingBroadcast: &listingBroadcastObservation{
			CommissionID: "9007199254740993", PublicRevision: 2, Stage: "send", BroadcastID: "201",
		},
	}
}

func TestListingBroadcastMaintenanceContract(t *testing.T) {
	e := listingBroadcastTestEvent()
	if err := validateMaintenanceEvent(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*maintenanceEvent){
		"invalid ID":           func(e *maintenanceEvent) { e.ListingBroadcast.CommissionID = "090" },
		"missing broadcast ID": func(e *maintenanceEvent) { e.ListingBroadcast.BroadcastID = "" },
		"unknown raw error":    func(e *maintenanceEvent) { e.ListingBroadcast.Reason = "/a/private/file" },
		"wrong phase":          func(e *maintenanceEvent) { e.Phase = "check" },
		"order metadata":       func(e *maintenanceEvent) { e.Commission = &commissionMaintenanceData{} },
		"wrong stage":          func(e *maintenanceEvent) { e.ListingBroadcast.Stage = "queue" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := listingBroadcastTestEvent()
			mutate(&bad)
			if validateMaintenanceEvent(bad, time.Now()) == nil {
				t.Fatal("accepted an invalid listing observation")
			}
		})
	}
	e.Result = "skipped"
	e.ListingBroadcast = &listingBroadcastObservation{
		CommissionID: "90", DraftVersion: 3, NotificationID: "123", Stage: "queue", Reason: "binding_missing",
	}
	if err := validateMaintenanceEvent(e, time.Now()); err != nil {
		t.Fatalf("valid queue skip rejected: %v", err)
	}
	e.ListingBroadcast.Stage = "handoff"
	for _, reason := range []string{"main_notice_transport_unverified", "main_session_mismatch", "execution_unconfirmed"} {
		e.ListingBroadcast.Reason = reason
		if err := validateMaintenanceEvent(e, time.Now()); err != nil {
			t.Errorf("valid handoff skip %q rejected: %v", reason, err)
		}
	}
}
