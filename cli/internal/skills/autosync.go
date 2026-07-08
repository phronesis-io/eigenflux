package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// AutoSyncStateFile is the small sidecar that records the last background
// auto-sync attempt so a heartbeat can throttle to at most once per TTL without
// re-hitting R2 on every poll. It lives in the eigenflux home dir (NOT the
// skills dir), so it is decoupled from the atomic skills-dir swap and the CLI
// version.
const AutoSyncStateFile = "skills-autosync.json"

// AutoSyncState is persisted between heartbeats.
type AutoSyncState struct {
	// LastAttemptUnix is when the last sync was *attempted* (success or not).
	// Recording attempt (not success) means an offline stretch can't punch
	// through the TTL gate on every heartbeat.
	LastAttemptUnix int64 `json:"last_attempt_unix"`
	// LastRevision / LastResult are informational (surfaced by `doctor`).
	LastRevision string `json:"last_revision,omitempty"`
	LastResult   string `json:"last_result,omitempty"` // ok|nochange|offline|error|disabled
}

// LoadAutoSyncState reads the state file from homeDir. A missing or corrupt file
// yields a zero state (treated as "never attempted") — never an error, so a bad
// file can only cost one extra sync attempt, never wedge the gate.
func LoadAutoSyncState(homeDir string) AutoSyncState {
	b, err := os.ReadFile(filepath.Join(homeDir, AutoSyncStateFile))
	if err != nil {
		return AutoSyncState{}
	}
	var s AutoSyncState
	if json.Unmarshal(b, &s) != nil {
		return AutoSyncState{}
	}
	return s
}

// SaveAutoSyncState writes the state file atomically (temp + rename) so a
// concurrent reader never sees a half-written file.
func SaveAutoSyncState(homeDir string, s AutoSyncState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(homeDir, ".skills-autosync-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, filepath.Join(homeDir, AutoSyncStateFile))
}

// DueForAutoSync reports whether a background sync should be attempted now.
// True when: never attempted (zero); now-last >= ttl; or last is in the future
// (clock rewind / corruption) so a bad timestamp can never wedge auto-sync off.
func DueForAutoSync(s AutoSyncState, ttl time.Duration, now time.Time) bool {
	if s.LastAttemptUnix <= 0 {
		return true
	}
	last := time.Unix(s.LastAttemptUnix, 0)
	if last.After(now) {
		return true
	}
	return now.Sub(last) >= ttl
}
