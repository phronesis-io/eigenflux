package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Credentials struct {
	AccessToken string `json:"access_token"`
	Email       string `json:"email,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
}

func credentialsPath(serverName string) string {
	home := os.Getenv("EIGENFLUX_HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = filepath.Join(h, ".eigenflux")
	}
	return filepath.Join(home, "servers", serverName, "credentials.json")
}

func LoadCredentials(serverName string) (*Credentials, error) {
	path := credentialsPath(serverName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no credentials for server %q: %w", serverName, err)
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	return &creds, nil
}

func SaveCredentials(serverName string, creds *Credentials) error {
	path := credentialsPath(serverName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// DeleteCredentials removes the credentials file for the given server.
func DeleteCredentials(serverName string) error {
	return os.Remove(credentialsPath(serverName))
}

func (c *Credentials) IsExpired() bool {
	if c.ExpiresAt == 0 {
		return false
	}
	return time.Now().UnixMilli() > c.ExpiresAt
}
