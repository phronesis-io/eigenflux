package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/config"
)

// WithV2CredentialsLock serializes refresh rotation across CLI processes for
// one server. The callback must reload credentials after acquiring the lock.
// Keep the file in place: unlinking it could let processes lock different inodes.
func WithV2CredentialsLock(serverName string, wait time.Duration, callback func() error) error {
	dir := filepath.Join(config.HomeDir(), "servers", serverName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, ".agent-v2-refresh.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("cannot open Agent V2 credential refresh lock %q: %w", path, err)
	}
	defer file.Close()
	deadline := time.Now().Add(wait)
	for {
		locked, err := tryV2CredentialsLock(file)
		if err != nil {
			return fmt.Errorf("cannot acquire Agent V2 credential refresh lock %q: %w", path, err)
		}
		if locked {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("timed out waiting for Agent V2 credential refresh lock %q", path)
		}
		time.Sleep(min(100*time.Millisecond, remaining))
	}
	defer unlockV2Credentials(file)
	return callback()
}
