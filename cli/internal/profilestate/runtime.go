package profilestate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RuntimeState stores installation identity and its report cache separately
// from config.json, whose unrelated writers may hold an older config snapshot.
// Identity is server-scoped; ReportedSnapshot also includes the account ID.
type RuntimeState struct {
	Host             string `json:"host"`
	Mode             string `json:"mode"`
	Revision         uint64 `json:"revision"`
	ReportedSnapshot string `json:"reported_snapshot,omitempty"`
	ReportedClient   string `json:"reported_client,omitempty"`
	ReportedAtMillis int64  `json:"reported_at_millis,omitempty"`
}

func runtimeFilePath(homeDir, serverName string) string {
	return filepath.Join(homeDir, "runtime-"+ScopeID(serverName, "")+".json")
}

func LoadRuntime(homeDir, serverName string) (RuntimeState, error) {
	b, err := os.ReadFile(runtimeFilePath(homeDir, serverName))
	if os.IsNotExist(err) {
		return RuntimeState{}, nil
	}
	if err != nil {
		return RuntimeState{}, err
	}
	var state RuntimeState
	if err := json.Unmarshal(b, &state); err != nil {
		return RuntimeState{}, fmt.Errorf("parse runtime state: %w", err)
	}
	return state, nil
}

// UpdateRuntime reuses the profile sidecar's process lock and atomic rename.
// Callbacks execute only while locked; callers must keep network I/O outside.
func UpdateRuntime(homeDir, serverName string, mutate func(*RuntimeState) (bool, error)) (RuntimeState, error) {
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return RuntimeState{}, err
	}
	path := runtimeFilePath(homeDir, serverName)
	lock, err := acquireLock(path + ".lock")
	if err != nil {
		return RuntimeState{}, err
	}
	defer lock.release()
	state, err := LoadRuntime(homeDir, serverName)
	if err != nil {
		return RuntimeState{}, err
	}
	changed, err := mutate(&state)
	if err != nil {
		return RuntimeState{}, err
	}
	if changed {
		if err := saveJSONFile(homeDir, path, state); err != nil {
			return RuntimeState{}, err
		}
	}
	return state, nil
}
