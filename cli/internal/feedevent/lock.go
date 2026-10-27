package feedevent

import (
	"path/filepath"

	"cli.eigenflux.ai/internal/filelock"
)

type lock struct{ held *filelock.Lock }

func acquireLock(dir string) (*lock, bool) {
	held, ok, err := filelock.TryAcquire(filepath.Join(dir, lockFileName))
	if err != nil || !ok {
		return nil, false
	}
	return &lock{held: held}, true
}
func (l *lock) release() {
	if l != nil {
		l.held.Release()
	}
}
