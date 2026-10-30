//go:build windows

package workbuddy

import (
	"context"
	"encoding/json"
	"github.com/Microsoft/go-winio"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsControlPipeRejectsUntrustedNames(t *testing.T) {
	for _, p := range []string{`\\remote\pipe\workbuddy-test`, `C:\host.sock`, `\\.\pipe\other`, "\\\\.\\pipe\\workbuddy-a\n", `\\.\pipe\workbuddy-a\nested`} {
		if socketOwner(p) == nil {
			t.Fatalf("accepted %q", p)
		}
	}
}
func TestWindowsControlPipeChecksSameUserAndExchangesRPC(t *testing.T) {
	path := `\\.\pipe\workbuddy-eigenflux-test-` + time.Now().Format("150405.000000000")
	l, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, e := l.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		var request map[string]any
		if json.NewDecoder(c).Decode(&request) == nil {
			_ = json.NewEncoder(c).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"sessions": []any{}}})
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err = sidecar(ctx, path, "session.list", map[string]any{}); err != nil {
		t.Fatal(err)
	}
}
func TestWindowsManagedNodeDiscovery(t *testing.T) {
	config := t.TempDir()
	dir := filepath.Join(config, "binaries", "node", "versions", "22.22.2")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(dir, "node.exe")
	if err := os.WriteFile(expected, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := discoverNode(config)
	if err != nil || got != expected {
		t.Fatalf("got %q, %v", got, err)
	}
}
func TestWindowsPipeUUIDValidation(t *testing.T) {
	for _, s := range []string{"", "../host", "abcdefgh", "aabbccdd\n"} {
		if pipeUUID.MatchString(s) {
			t.Fatalf("accepted %q", s)
		}
	}
	if !pipeUUID.MatchString("0123abcd") {
		t.Fatal("valid uuid rejected")
	}
}

func TestWindowsHostTempPrecedence(t *testing.T) {
	t.Setenv("TEMP", `C:\中文用户\Temp`)
	t.Setenv("TMP", `C:\different`)
	if windowsHostTempDir() != `C:\中文用户\Temp` {
		t.Fatal(windowsHostTempDir())
	}
}
func TestWindowsNodeVersionOrder(t *testing.T) {
	if !nodeVersionLess("v9.20.0", "v22.22.2") || !nodeVersionLess("22.9.0", "22.22.2") {
		t.Fatal("Node versions not ordered numerically")
	}
}
