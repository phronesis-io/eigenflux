//go:build !darwin && !windows

package dispatch

import (
	"context"
	"errors"
	"testing"
)

func TestCodexOwnerIPCUnsupportedPlatform(t *testing.T) {
	_, err := openCodexOwnerIPC(context.Background(), Binding{Host: "codex", Mode: "native", Env: map[string]string{"CODEX_HOME": "/unused"}})
	if !errors.Is(err, errCodexOwnerIPCUnsupported) {
		t.Fatalf("unverified platform guessed an IPC transport: %v", err)
	}
}
