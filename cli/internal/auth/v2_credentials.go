package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"cli.eigenflux.ai/internal/config"
)

type V2Credentials struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	AgentID      string   `json:"agent_id"`
	PrincipalID  string   `json:"principal_id"`
	ExpiresAt    int64    `json:"expires_at"`
	Scopes       []string `json:"scopes,omitempty"`
}

func v2CredentialsPath(serverName string) string {
	return filepath.Join(config.HomeDir(), "servers", serverName, "agent-v2-credentials.json")
}

func LoadV2Credentials(serverName string) (*V2Credentials, error) {
	data, err := os.ReadFile(v2CredentialsPath(serverName))
	if err != nil {
		return nil, fmt.Errorf("no Agent V2 credentials for server %q: %w", serverName, err)
	}
	var credentials V2Credentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		return nil, fmt.Errorf("parse Agent V2 credentials: %w", err)
	}
	if credentials.AccessToken == "" || credentials.RefreshToken == "" || credentials.AgentID == "" {
		return nil, fmt.Errorf("Agent V2 credentials for server %q are incomplete", serverName)
	}
	return &credentials, nil
}

func SaveV2Credentials(serverName string, credentials *V2Credentials) error {
	path := v2CredentialsPath(serverName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func DeleteV2Credentials(serverName string) error {
	err := os.Remove(v2CredentialsPath(serverName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
