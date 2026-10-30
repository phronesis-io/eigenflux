package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/maintenance"
	watchstate "cli.eigenflux.ai/internal/watch"
)

const notificationPermissionCheckInterval = time.Minute
const notificationPermissionRenewInterval = 5 * time.Minute

var notificationDeviceIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type notificationDevice struct {
	ID string `json:"device_id"`
}

// The random per-Home identifier survives upgrades without exposing hardware IDs.
// Copying a Home also copies its installation identity; it is not a hardware ID.
func notificationDeviceAt(home string) (notificationDevice, error) {
	path := filepath.Join(home, "notifications", "device.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return notificationDevice{}, err
	}
	release, err := watchstate.AcquirePath(path + ".lock")
	if err != nil {
		return notificationDevice{}, err
	}
	defer release()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		device := notificationDevice{ID: maintenance.NewID()}
		return device, dispatch.WriteJSON(path, device)
	}
	if err != nil {
		return notificationDevice{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return notificationDevice{}, errors.New("invalid notification device identity")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return notificationDevice{}, err
	}
	var device notificationDevice
	if json.Unmarshal(raw, &device) != nil || !notificationDeviceIDPattern.MatchString(device.ID) {
		return notificationDevice{}, errors.New("invalid notification device identity")
	}
	return device, nil
}

func notificationDeviceName(hostname, platform string) string {
	if !utf8.ValidString(hostname) {
		hostname = ""
	}
	runes := []rune(strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return -1
		}
		return r
	}, hostname)))
	if len(runes) > 64 {
		runes = runes[:64]
	}
	name := strings.TrimSpace(string(runes))
	if name == "" {
		if platform == "windows" {
			return "Windows PC"
		}
		return "Mac"
	}
	return name
}

type notificationPermissionState struct {
	fingerprint string
	recordedAt  time.Time
}

func (s *notificationPermissionState) record(home, server, agent string, device notificationDevice, hostname string, observation desktopnotify.Observation) error {
	observation.DeviceID = device.ID
	observation.DeviceName = notificationDeviceName(hostname, observation.Platform)
	fingerprint := strings.Join([]string{observation.Platform, observation.Result, observation.Code, observation.Stage, observation.HResult, observation.DeviceID, observation.DeviceName}, "\x00")
	if fingerprint == s.fingerprint && observation.At.Sub(s.recordedAt) < notificationPermissionRenewInterval {
		return nil
	}
	observation.EventID, observation.AgentID, observation.Source = maintenance.NewID(), agent, "watch"
	observation.CLIVersion, observation.CLICommit = version, commit
	if err := maintenance.Validate(notificationMaintenanceEvent(observation)); err != nil {
		return err
	}
	if err := appendNotificationObservation(home, server, observation); err != nil {
		return err
	}
	s.fingerprint, s.recordedAt = fingerprint, observation.At
	return nil
}

func (w *accountWatch) notificationPermissionLoop(ctx context.Context) error {
	if w.desktop == nil || w.desktopProvider == nil || !nativeDesktopSupported() {
		return nil
	}
	var state notificationPermissionState
	for ctx.Err() == nil {
		if err := w.dispatchIdentityOK(); err != nil {
			return err
		}
		device, err := notificationDeviceAt(w.home)
		if err == nil {
			var observation desktopnotify.Observation
			probe := desktopnotify.NewWithObserver(func(e desktopnotify.Observation) { observation = e })
			call, cancel := context.WithTimeout(ctx, 10*time.Second)
			// Check never registers a sender, requests permission or displays UI.
			_ = probe.Check(call)
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := w.dispatchIdentityOK(); err != nil {
				return err
			}
			hostname, _ := os.Hostname()
			if observation.Platform != runtime.GOOS {
				return errors.New("notification permission platform mismatch")
			}
			err = state.record(w.home, w.server.Name, w.identity.AgentID, device, hostname, observation)
		}
		if err != nil {
			if emitErr := w.emit("notification_telemetry_status", map[string]string{"status": "queued", "code": "permission_observation_unavailable"}); emitErr != nil {
				return emitErr
			}
		}
		if !watchPause(ctx, notificationPermissionCheckInterval) {
			break
		}
	}
	return ctx.Err()
}
