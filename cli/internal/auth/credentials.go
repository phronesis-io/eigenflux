package auth

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/config"
)

type Credentials struct {
	AccessToken string `json:"access_token"`
	Email       string `json:"email,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
}

func credentialsPath(serverName string) string {
	return filepath.Join(config.HomeDir(), "servers", serverName, "credentials.json")
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: temp file + rename to avoid partial-write corruption.
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// DeleteCredentials removes the credentials file for the given server.
// Returns nil if the file does not exist.
func DeleteCredentials(serverName string) error {
	err := os.Remove(credentialsPath(serverName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (c *Credentials) IsExpired() bool {
	if c.ExpiresAt == 0 {
		return false
	}
	return time.Now().UnixMilli() > c.ExpiresAt
}

const sessionDurationMs = int64(30 * 24 * time.Hour / time.Millisecond)

// RefreshExpiry updates the local expires_at to now + 30 days,
// mirroring the server-side sliding expiration.
// RefreshExpiry extends the stored session expiry for the given server
// to 30 days from now. It is best-effort: errors are logged at debug level
// but do not interrupt the caller.
func RefreshExpiry(serverName string) {
	creds, err := LoadCredentials(serverName)
	if err != nil {
		log.Printf("auth: refresh expiry: load credentials for %q: %v", serverName, err)
		return
	}
	creds.ExpiresAt = time.Now().UnixMilli() + sessionDurationMs
	if err := SaveCredentials(serverName, creds); err != nil {
		log.Printf("auth: refresh expiry: save credentials for %q: %v", serverName, err)
	}
}
