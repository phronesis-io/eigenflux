package controlcontext

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"cli.eigenflux.ai/internal/config"
)

type Snapshot struct {
	OwnerAgentID string          `json:"owner_agent_id"`
	Revision     int64           `json:"context_revision"`
	Context      json.RawMessage `json:"control_context"`
}

func pathFor(serverName string) string {
	return filepath.Join(config.HomeDir(), "servers", serverName, "control-context.json")
}

func Load(serverName, ownerAgentID string) (Snapshot, error) {
	data, err := os.ReadFile(pathFor(serverName))
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if json.Unmarshal(data, &snapshot) != nil || snapshot.OwnerAgentID == "" ||
		snapshot.OwnerAgentID != ownerAgentID || snapshot.Revision <= 0 || len(snapshot.Context) == 0 {
		return Snapshot{}, fmt.Errorf("invalid control-context cache")
	}
	return snapshot, nil
}

func Save(serverName string, snapshot Snapshot) error {
	if snapshot.OwnerAgentID == "" || snapshot.Revision <= 0 || len(snapshot.Context) == 0 {
		return fmt.Errorf("control context requires an owner, positive revision, and payload")
	}
	path := pathFor(serverName)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".control-context-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// Delete removes a cached immutable context when the server reports that the
// current Agent has not completed onboarding. This prevents an older identity
// using the same local server name from being reported as applied.
func Delete(serverName string) error {
	err := os.Remove(pathFor(serverName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
