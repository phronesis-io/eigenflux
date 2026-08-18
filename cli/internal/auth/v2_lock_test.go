package auth

import (
	"sync"
	"testing"
	"time"
)

func TestV2CredentialsLockSerializesRefreshWriters(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		errors <- WithV2CredentialsLock("review", 2*time.Second, func() error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered
	go func() {
		defer wait.Done()
		errors <- WithV2CredentialsLock("review", 2*time.Second, func() error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second refresh writer entered before the first released the lock")
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseFirst)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
}
