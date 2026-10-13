package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
)

func TestV2CredentialsLockReleasesAfterWindowsReaderCloses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", home)
	path := filepath.Join(config.HomeDir(), "servers", "review", ".agent-v2-refresh.lock")
	closed := make(chan struct{})
	err := WithV2CredentialsLock("review", time.Second, func() error {
		reader, err := os.Open(path)
		if err != nil {
			return err
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			_ = reader.Close()
			close(closed)
		}()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-closed
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("released lock remains: %v", err)
	}
	if err := WithV2CredentialsLock("review", time.Second, func() error { return nil }); err != nil {
		t.Fatalf("next refresh blocked: %v", err)
	}
}
