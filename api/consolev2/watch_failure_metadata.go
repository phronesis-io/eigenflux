package consolev2

import (
	"encoding/json"
	"time"
)

const watchFailuresHeader = "X-EigenFlux-Watch-Failures"

func parseWatchFailureMetadata(raw string) map[string]string {
	if raw == "" || len(raw) > 8192 {
		return nil
	}
	var reasons map[string]string
	if err := json.Unmarshal([]byte(raw), &reasons); err != nil || len(reasons) > 50 {
		return nil
	}
	return reasons
}

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
