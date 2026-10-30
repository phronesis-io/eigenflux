package auth

import (
	"cli.eigenflux.ai/internal/config"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestV2CredentialsLockRetainsFileAndReleasesOnError(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	dir := filepath.Join(config.HomeDir(), "servers", "review")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".agent-v2-refresh.lock")
	if err := os.WriteFile(path, []byte("123 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	cause := errors.New("refresh failed")
	if err := WithV2CredentialsLock("review", time.Second, func() error { return cause }); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock file replaced or removed: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "123 1\n" {
		t.Fatalf("lock changed: %q %v", data, err)
	}
	if err := WithV2CredentialsLock("review", time.Second, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
func TestV2CredentialsLockProcessExit(t *testing.T) {
	if os.Getenv("EF_LOCK_CHILD") == "1" {
		err := WithV2CredentialsLock("review", time.Second, func() error {
			if err := os.WriteFile(os.Getenv("EF_LOCK_READY"), nil, 0600); err != nil {
				return err
			}
			for {
				time.Sleep(time.Second)
			}
		})
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", home)
	ready := filepath.Join(home, "ready")
	child := exec.Command(os.Args[0], "-test.run=^TestV2CredentialsLockProcessExit$")
	child.Env = append(os.Environ(), "EF_LOCK_CHILD=1", "EF_LOCK_READY="+ready)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = child.Process.Kill()
		if child.ProcessState == nil {
			_ = child.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not acquire lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	err := WithV2CredentialsLock("review", 50*time.Millisecond, func() error { t.Fatal("entered held lock"); return nil })
	if !errors.Is(err, ErrV2CredentialsLockTimeout) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if err := WithV2CredentialsLock("review", time.Second, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
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

func TestV2CredentialsLockContextCancelsWhileHeld(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	err := WithV2CredentialsLock("review", time.Second, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := WithV2CredentialsLockContext(ctx, "review", time.Second, func() error {
			t.Error("cancelled waiter entered held lock")
			return nil
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("want context deadline, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := WithV2CredentialsLock("review", time.Second, func() error { return nil }); err != nil {
		t.Fatalf("lock not released after cancelled waiter: %v", err)
	}
}

func TestV2CredentialsLockContextAlreadyCancelled(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := WithV2CredentialsLockContext(ctx, "review", time.Second, func() error {
		t.Error("cancelled caller entered lock")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context cancellation, got %v", err)
	}
	path := filepath.Join(config.HomeDir(), "servers", "review", ".agent-v2-refresh.lock")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cancelled caller created lock: %v", err)
	}
}
