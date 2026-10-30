// Package filelock provides nonblocking OS locks with retained lock files.
package filelock

import (
	"os"
	"sync"
)

type Lock struct {
	file *os.File
	once sync.Once
}

// TryAcquire leaves the pathname intact. A process exit releases the OS lock.
// A false acquired result means contention; other failures retain their cause.
func TryAcquire(path string) (*Lock, bool, error) {
	return acquire(path, os.O_CREATE|os.O_RDWR)
}

func acquire(path string, flags int) (*Lock, bool, error) {
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, false, err
	}
	ok, err := tryLock(f)
	if err != nil || !ok {
		_ = f.Close()
		return nil, ok, err
	}
	return &Lock{file: f}, true, nil
}

func (l *Lock) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() { unlock(l.file); _ = l.file.Close() })
}

// Idle probes an existing file without creating or modifying it. Presence alone
// does not imply ownership; abandoned marker contents have no locking meaning.
func Idle(path string) (bool, error) {
	l, ok, err := acquire(path, os.O_RDONLY)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if l != nil {
		l.Release()
	}
	return ok, nil
}
