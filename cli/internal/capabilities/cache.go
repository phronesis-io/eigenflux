package capabilities

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"cli.eigenflux.ai/internal/config"
)

type Snapshot struct {
	OwnerAgentID  string          `json:"owner_agent_id"`
	ServerURL     string          `json:"server_url"`
	Language      string          `json:"language"`
	ETag          string          `json:"etag"`
	FetchedAt     int64           `json:"fetched_at"`
	MaxAgeSeconds int64           `json:"max_age_seconds"`
	Registry      json.RawMessage `json:"registry"`
}

func pathFor(serverName, language string) string {
	return filepath.Join(config.HomeDir(), "servers", serverName, "capabilities-"+language+".json")
}

func Load(serverName, serverURL, ownerAgentID, language string) (Snapshot, error) {
	data, err := os.ReadFile(pathFor(serverName, language))
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if json.Unmarshal(data, &snapshot) != nil || snapshot.OwnerAgentID != ownerAgentID || snapshot.ServerURL != serverURL ||
		snapshot.Language != language || snapshot.ETag == "" || snapshot.MaxAgeSeconds <= 0 || len(snapshot.Registry) == 0 {
		return Snapshot{}, fmt.Errorf("invalid capability registry cache")
	}
	return snapshot, nil
}

func Save(serverName string, snapshot Snapshot) error {
	if snapshot.OwnerAgentID == "" || snapshot.ServerURL == "" || snapshot.Language == "" || snapshot.ETag == "" || snapshot.FetchedAt <= 0 || snapshot.MaxAgeSeconds <= 0 || len(snapshot.Registry) == 0 {
		return fmt.Errorf("capability registry cache is incomplete")
	}
	path := pathFor(serverName, snapshot.Language)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".capabilities-*.tmp")
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
