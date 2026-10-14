package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/watch"
)

const maxNotificationObservations = 256

type notificationObservations struct {
	Version int                         `json:"version"`
	Events  []desktopnotify.Observation `json:"events"`
}

func notificationServer() string {
	if serverFlag != "" {
		return serverFlag
	}
	cfg, err := config.Load()
	if err == nil && cfg.DefaultServer != "" {
		return cfg.DefaultServer
	}
	return "eigenflux"
}

func observedNotificationCLI() *desktopnotify.Notifier {
	return observedNotificationCLIFrom("manual")
}

func observedNotificationCLIFrom(source string) *desktopnotify.Notifier {
	server := notificationServer()
	agent := ""
	if credentials, err := auth.LoadV2Credentials(server); err == nil {
		agent = credentials.AgentID
	}
	return observedDesktopNotifierAt(config.HomeDir(), server, agent, source)
}

func observedDesktopNotifierAt(home, server, agent, source string) *desktopnotify.Notifier {
	return desktopnotify.NewWithObserver(func(e desktopnotify.Observation) {
		e.EventID, e.AgentID, e.Source = maintenance.NewID(), agent, source
		e.CLIVersion, e.CLICommit = version, commit
		if err := appendNotificationObservation(home, server, e); err != nil {
			// Telemetry failure must not turn a submitted notification into a retry.
			fmt.Fprintln(os.Stderr, "Local notification observation could not be saved.")
		}
	})
}

// Import only records pinned to this account. The sole unbound handoff is setup
// recorded before authentication; an unbound send is never reassigned.
func importNotificationObservations(scope maintenance.Scope) error {
	path := notificationLogPath(scope.Home, scope.Server)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	release, err := watch.AcquirePath(path + ".lock")
	if err != nil {
		return err
	}
	defer release()
	state, err := readNotificationObservations(scope.Home, scope.Server)
	if err != nil {
		return err
	}
	changed := false
	for i := range state.Events {
		e := &state.Events[i]
		if e.Platform != "windows" && e.Platform != "darwin" {
			continue
		}
		if e.Queued || e.EventID == "" || e.At.Before(time.Now().Add(-maintenance.Retention)) {
			continue
		}
		if e.AgentID != "" && e.AgentID != scope.AgentID {
			continue
		}
		if e.AgentID == "" && e.Action != "setup" {
			continue
		}
		event := notificationMaintenanceEvent(*e)
		if err := maintenance.Record(scope, event); err != nil {
			return err
		}
		e.AgentID, e.Queued = scope.AgentID, true
		changed = true
	}
	if changed {
		return dispatch.WriteJSON(path, state)
	}
	return nil
}

func notificationMaintenanceEvent(e desktopnotify.Observation) maintenance.Event {
	trigger := "manual"
	if e.Source == "watch" {
		trigger = "auto"
	}
	event := maintenance.NewEvent(e.EventID, "notifier", trigger, "notification", e.Result)
	event.EventAt = e.At.UnixMilli()
	event.Source, event.Platform, event.ErrorCode, event.FromVersion, event.DurationMS = e.Source, e.Platform, e.Code, e.CLIVersion, e.DurationMS
	event.Notification = &maintenance.Notification{Action: e.Action, Visibility: e.Visibility, IDHash: e.NotificationIDHash, Stage: e.Stage, HResult: e.HResult, CLICommit: e.CLICommit}
	return event
}

func flushNotificationTelemetry(server string) string {
	scope, err := maintenanceScope(server)
	if err != nil || scope.AgentID == "" {
		return "pending_auth"
	}
	if err := flushMaintenanceEvents(server); err != nil {
		return "queued"
	}
	state, err := maintenance.Snapshot(scope)
	if err != nil {
		return "queued"
	}
	for _, e := range state.Events {
		if e.Phase == "notification" {
			return "queued"
		}
	}
	return "reported"
}

func notificationLogPath(home, server string) string {
	return filepath.Join(home, "notifications", "events-"+watch.Scope(home, server, "", "")+".json")
}

func readNotificationObservations(home, server string) (notificationObservations, error) {
	state := notificationObservations{Version: 1, Events: []desktopnotify.Observation{}}
	path := notificationLogPath(home, server)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Size() > 512<<10 {
		return state, errors.New("invalid notification observation file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if json.Unmarshal(raw, &state) != nil || state.Version != 1 || state.Events == nil || len(state.Events) > maxNotificationObservations {
		return state, errors.New("invalid notification observation state")
	}
	return state, nil
}

func appendNotificationObservation(home, server string, e desktopnotify.Observation) error {
	path := notificationLogPath(home, server)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	release, err := watch.AcquirePath(path + ".lock")
	if err != nil {
		return err
	}
	defer release()
	state, err := readNotificationObservations(home, server)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	kept := []desktopnotify.Observation{}
	for _, previous := range state.Events {
		if previous.At.After(cutoff) {
			kept = append(kept, previous)
		}
	}
	kept = append(kept, e)
	if len(kept) > maxNotificationObservations {
		kept = kept[len(kept)-maxNotificationObservations:]
	}
	state.Events = kept
	return dispatch.WriteJSON(path, state)
}
