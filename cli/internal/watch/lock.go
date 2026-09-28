// Package watch contains the local lifecycle primitives for a single account loop.
package watch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

func Scope(home, server, agent, principal string) string {
	b := sha256.Sum256([]byte(home + "\x00" + server + "\x00" + agent + "\x00" + principal))
	return hex.EncodeToString(b[:16])
}

func CanonicalHome(home string) (string, error) {
	p, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(p, 0700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// Acquire locks Home/server, independently of the identity currently bound to it.
// The OS releases the lock after a crash; the pathname is retained to avoid an
// unlink/recreate race between owners.
func Acquire(home, server string) (func(), error) {
	dir := filepath.Join(home, "watch")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return AcquirePath(filepath.Join(dir, Scope(home, server, "", "")+".lock"))
}

func AcquirePath(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("watch already active or lock unavailable: %w", err)
	}
	return func() { unlock(f); f.Close() }, nil
}
