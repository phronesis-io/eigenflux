package profilestate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// State is the CLI-owned freshness state for one server/account pair. It is
// deliberately kept outside config.json: feed polling and settings sync are
// separate processes, and using the shared config read-modify-write path can
// silently lose a just-completed profile evaluation.
type State struct {
	LastRefreshUnix int64 `json:"last_refresh_unix"`
	LastCheckedUnix int64 `json:"last_checked_unix"`
}

// FilePath returns a stable, path-safe per-server/per-agent state file.
func FilePath(homeDir, serverName, agentID string) string {
	scope := ScopeID(serverName, agentID)
	return filepath.Join(homeDir, "profile-refresh-"+scope+".json")
}

// ScopeID is the non-secret identifier shared with host adapters so they can
// keep retry state beside, but never inspect, CLI credentials.
func ScopeID(serverName, agentID string) string {
	sum := sha256.Sum256([]byte(serverName + "\x00" + agentID))
	return hex.EncodeToString(sum[:8])
}

// Load treats a missing or corrupt sidecar as zero state. The caller seeds a
// zero state instead of prompting immediately, so corruption cannot create a
// fleet-wide refresh storm.
func Load(homeDir, serverName, agentID string) State {
	b, err := os.ReadFile(FilePath(homeDir, serverName, agentID))
	if err != nil {
		return State{}
	}
	var state State
	if json.Unmarshal(b, &state) != nil {
		return State{}
	}
	return state
}

// Save writes atomically so concurrent readers never observe truncated JSON.
func Save(homeDir, serverName, agentID string, state State) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(homeDir, ".profile-refresh-*.tmp")
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
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, FilePath(homeDir, serverName, agentID)); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
