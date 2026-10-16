package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cli.eigenflux.ai/internal/config"
)

// WithV2CredentialsLock serializes refresh rotation across CLI processes for
// one server. The callback must reload credentials after acquiring the lock.
func WithV2CredentialsLock(serverName string, wait time.Duration, callback func() error) error {
	return withV2CredentialsLock(serverName, wait, callback, os.Remove)
}

func withV2CredentialsLock(serverName string, wait time.Duration, callback func() error, remove func(string) error) (resultErr error) {
	dir := filepath.Join(config.HomeDir(), "servers", serverName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, ".agent-v2-refresh.lock")
	deadline := time.Now().Add(wait)
	pid := os.Getpid()
	var file *os.File
	for {
		created, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			file = created
			_, _ = fmt.Fprintf(file, "%d %d\n", pid, time.Now().Unix())
			_ = file.Sync()
			break
		}
		if !os.IsExist(err) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for Agent V2 credential refresh lock")
		}
		if staleV2CredentialLock(path) {
			if err := remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("cannot remove expired Agent V2 credential refresh lock %q; check the lock file and parent directory permissions, ownership, and host application sandbox access; keep agent-v2-credentials.json intact: %w", path, err)
			}
			continue
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer func() {
		_ = file.Close()
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		fields := strings.Fields(string(data))
		if len(fields) == 0 {
			return
		}
		owner, _ := strconv.Atoi(fields[0])
		if owner == pid {
			// A concurrent Windows reader temporarily prevents DeleteFile.
			// Retrying that sharing violation avoids leaving a live-looking lock.
			deadline := time.Now().Add(time.Second)
			for {
				err := remove(path)
				if err == nil || os.IsNotExist(err) {
					return
				}
				if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(32)) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				resultErr = errors.Join(resultErr, fmt.Errorf("cannot release Agent V2 credential refresh lock %q; keep agent-v2-credentials.json intact: %w", path, err))
				return
			}
		}
	}()
	return callback()
}

func staleV2CredentialLock(path string) bool {
	data, err := os.ReadFile(path)
	if err == nil {
		fields := strings.Fields(string(data))
		if len(fields) == 2 {
			if createdAt, parseErr := strconv.ParseInt(fields[1], 10, 64); parseErr == nil {
				return time.Now().Unix()-createdAt > 120
			}
		}
	}
	info, err := os.Stat(path)
	return err == nil && time.Since(info.ModTime()) > 2*time.Minute
}
