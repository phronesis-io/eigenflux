package skills

import (
	"path/filepath"

	"cli.eigenflux.ai/internal/filelock"
)

// Lock is retained on disk; only the OS lock, not file presence or age, denotes
// an active writer. All CLI processes sharing this target must use OS locking.
type Lock = filelock.Lock

func acquireLock(parent string) (*Lock, bool, error) {
	return filelock.TryAcquire(filepath.Join(parent, lockFileName))
}
