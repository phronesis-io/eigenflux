package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/config"
)

// ErrV2CredentialsLockTimeout marks local lock contention without conflating it
// with account changes, permission failures or the caller's cancellation.
var ErrV2CredentialsLockTimeout = errors.New("timed out waiting for Agent V2 credential refresh lock")

// WithV2CredentialsLock serializes refresh rotation across CLI processes for
// one server. The callback must reload credentials after acquiring the lock.
// Keep the file in place: unlinking it could let processes lock different inodes.
func WithV2CredentialsLock(serverName string, wait time.Duration, callback func() error) error {
	return WithV2CredentialsLockContext(context.Background(), serverName, wait, callback)
}

// WithV2CredentialsLockContext also cancels lock acquisition when the caller stops.
func WithV2CredentialsLockContext(ctx context.Context, serverName string, wait time.Duration, callback func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
		if err := ctx.Err(); err != nil {
			return err
		}
		locked, err := tryV2CredentialsLock(file)
		if err != nil {
			return fmt.Errorf("cannot acquire Agent V2 credential refresh lock %q: %w", path, err)
		}
		if locked {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("%w %q", ErrV2CredentialsLockTimeout, path)
		}
		timer := time.NewTimer(min(100*time.Millisecond, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer unlockV2Credentials(file)
	if err := ctx.Err(); err != nil {
		return err
	}
	return callback()
}
