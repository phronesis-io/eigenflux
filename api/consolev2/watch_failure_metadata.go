package consolev2

import "time"

const watchFailuresHeader = "X-EigenFlux-Watch-Failures"

// Invalid optional metadata must not reject or change the original valid event.
func mergeWatchFailureMetadata(event maintenanceEvent, reason string, now time.Time) maintenanceEvent {
	if reason == "" || event.OuterLoop == nil {
		return event
	}
	candidate := event
	outer := *event.OuterLoop
	outer.FailureReason = reason
	candidate.OuterLoop = &outer
	if validateMaintenanceEvent(candidate, now) != nil {
		return event
	}
	return candidate
}
