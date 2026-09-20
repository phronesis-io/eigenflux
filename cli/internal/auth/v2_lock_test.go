package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
)

func TestV2CredentialsLockReportsExpiredLockRemovalFailure(t *testing.T) {
	for _, cause := range []error{os.ErrPermission, syscall.EPERM, errors.New("filesystem unavailable")} {
		t.Run(cause.Error(), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("EIGENFLUX_HOME", home)
			dir := filepath.Join(config.HomeDir(), "servers", "review")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".agent-v2-refresh.lock")
			if err := os.WriteFile(path, []byte("123 1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			credentialsPath := filepath.Join(dir, "agent-v2-credentials.json")
			if err := os.WriteFile(credentialsPath, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			err := withV2CredentialsLock("review", time.Second, func() error {
				t.Fatal("refresh must not run without the lock")
				return nil
			}, func(got string) error {
				calls++
				if calls > 1 {
					t.Fatal("removal failure must not be retried")
				}
				if got != path {
					t.Fatalf("remove path = %q", got)
				}
				return &os.PathError{Op: "remove", Path: got, Err: cause}
			})
			if !errors.Is(err, cause) {
				t.Fatalf("error = %v, want wrapped %v", err, cause)
			}
			for _, want := range []string{path, "permissions", "ownership", "sandbox", "keep agent-v2-credentials.json intact"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error missing %q: %v", want, err)
				}
			}
			if calls != 1 {
				t.Fatalf("remove calls = %d", calls)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(credentialsPath); err != nil || string(data) != "unchanged" {
				t.Fatalf("credentials changed: %q, %v", data, err)
			}
		})
	}
}

func TestV2CredentialsLockRecoversExpiredLock(t *testing.T) {
	for _, alreadyRemoved := range []bool{false, true} {
		t.Run(fmt.Sprint(alreadyRemoved), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("EIGENFLUX_HOME", home)
			dir := filepath.Join(config.HomeDir(), "servers", "review")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".agent-v2-refresh.lock")
			if err := os.WriteFile(path, []byte("123 1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			called := false
			err := withV2CredentialsLock("review", time.Second, func() error { called = true; return nil }, func(path string) error {
				if err := os.Remove(path); err != nil {
					return err
				}
				if alreadyRemoved {
					return &os.PathError{Op: "remove", Path: path, Err: os.ErrNotExist}
				}
				return nil
			})
			if err != nil || !called {
				t.Fatalf("called=%v error=%v", called, err)
			}
		})
	}
}

func TestV2CredentialsLockExpiredRemovalRespectsDeadline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", home)
	dir := filepath.Join(config.HomeDir(), "servers", "review")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".agent-v2-refresh.lock")
	if err := os.WriteFile(path, []byte("123 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := withV2CredentialsLock("review", -time.Second, func() error { t.Fatal("unexpected refresh"); return nil }, func(string) error { t.Fatal("expired deadline must stop removal retries"); return nil })
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error=%v", err)
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
