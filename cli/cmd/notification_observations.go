package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
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
	return observedDesktopNotifier(config.HomeDir(), notificationServer())
}

func observedDesktopNotifier(home, server string) *desktopnotify.Notifier {
	return desktopnotify.NewWithObserver(func(e desktopnotify.Observation) {
		e.CLIVersion, e.CLICommit = version, commit
		if err := appendNotificationObservation(home, server, e); err != nil {
			// Telemetry failure must not turn a submitted notification into a retry.
			fmt.Fprintln(os.Stderr, "Local notification observation could not be saved.")
		}
	})
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
