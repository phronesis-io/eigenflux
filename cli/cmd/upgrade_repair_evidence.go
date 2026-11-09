package cmd

import "cli.eigenflux.ai/internal/maintenance"

// A read-only cycle-check cannot turn a pre-trigger foreground completion into
// unattended acceptance. Continuations retain the original canonical trigger.
func upgradeRepairTriggerAt(scope maintenance.Scope, thread, turn string, c maintenance.Cycle) (int64, error) {
	if c.Result != "completed" || c.Mode != "full" {
		return 0, nil
	}
	currentAttempt := heartbeatHostAttempt(thread, turn)
	marker, ok := readHeartbeatHostMarker(scope, thread, currentAttempt)
	if !ok || marker.Cycle != "" && marker.Cycle != c.ID {
		return 0, nil
	}
	attempt := marker.OriginAttempt
	if attempt == "" {
		attempt = currentAttempt
	}
	at, err := maintenance.LifecycleObservedAt(scope, attempt, "trigger", "observed")
	if err != nil {
		return 0, err
	}
	if at == 0 || c.StartedAt <= at {
		return 0, nil
	}
	return at, nil
}
