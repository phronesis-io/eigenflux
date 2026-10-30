package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"runtime"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/desktopqueue"
	"cli.eigenflux.ai/internal/maintenance"
)

// Telemetry has its own durable bounded observation queue. Failure never retries
// an OS submission or changes business execution. Stable IDs deduplicate imports.
func (w *accountWatch) recordWatchNotification(entry desktopqueue.Entry, stage string, cause error) {
	if !nativeDesktopSupported() {
		return
	}
	trace := entry.Watch
	if trace == nil {
		// Older queue entries lack an order version/job identity. Do not guess them
		// from the current binding or a newer order snapshot.
		u, err := url.Parse(entry.Message.URL)
		if err != nil {
			return
		}
		trace = &maintenance.WatchNotification{Origin: "legacy", OrderID: u.Query().Get("order_id"), Role: u.Query().Get("role")}
	}
	copy := *trace
	copy.Attempt = 0
	result, code := "queued", ""
	if stage != "queued" {
		copy.Attempt = entry.Attempts + 1
		result = "submitted"
	}
	if stage == "prepare_failed" {
		result, code = "failed", "service_name_unavailable"
	}
	if stage == "os_failed" {
		result, code = "failed", watchNotificationFailureCode(cause)
	}
	hash := sha256.Sum256([]byte(entry.Message.ID))
	idHash := hex.EncodeToString(hash[:])
	key := sha256.Sum256([]byte(idHash + ":" + stage + ":" + strconv.Itoa(copy.Attempt)))
	eventID := hex.EncodeToString(key[:])
	if stage != "queued" {
		// Every real preparation/OS call gets an independent receipt. A crash
		// after Show but before Complete can repeat the queue retry counter;
		// that second physical submission must remain visible, not deduplicated.
		eventID = maintenance.NewID()
	}
	observation := desktopnotify.Observation{
		EventID: eventID, AgentID: w.identity.AgentID,
		Source: "watch", Platform: runtime.GOOS, At: time.Now().UTC(),
		Action: "watch", Result: result, Visibility: "unknown", Stage: stage,
		Code: code, NotificationIDHash: idHash, CLIVersion: version, CLICommit: commit, Watch: &copy,
	}
	if err := maintenance.Validate(notificationMaintenanceEvent(observation)); err != nil {
		w.emitWatchNotificationTelemetryFailure()
		return
	}
	if err := appendNotificationObservation(w.home, w.server.Name, observation); err != nil {
		w.emitWatchNotificationTelemetryFailure()
	}
}

func (w *accountWatch) emitWatchNotificationTelemetryFailure() {
	_ = w.emit("notification_telemetry_status", map[string]string{"status": "queued", "code": "local_queue_unavailable"})
}

func watchNotificationFailureCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, desktopnotify.ErrPermission):
		return "permission_denied"
	case errors.Is(err, desktopnotify.ErrPermissionRequired):
		return "permission_required"
	case errors.Is(err, desktopnotify.ErrHelperMissing):
		return "helper_missing"
	case errors.Is(err, desktopnotify.ErrUnsupported):
		return "unsupported_platform"
	case errors.Is(err, desktopnotify.ErrPendingFirstNotification):
		return "pending_first_notification"
	default:
		return "provider_failed"
	}
}
