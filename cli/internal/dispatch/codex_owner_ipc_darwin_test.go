//go:build darwin

package dispatch

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func ownerIPCTestSocket(t *testing.T) (Binding, *net.UnixListener) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "ef-owner-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	parent := filepath.Join(home, "ipc")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "ipc.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	return Binding{Host: "codex", Mode: "native", Env: map[string]string{"CODEX_HOME": home}}, listener
}

func TestOpenCodexOwnerIPCDarwinPrivatePeerAndInitialize(t *testing.T) {
	for _, variant := range []string{"handled_by", "without_handled_by", "mismatched_client"} {
		t.Run(variant, func(t *testing.T) {
			binding, listener := ownerIPCTestSocket(t)
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				raw, err := readCodexOwnerIPCFrame(conn)
				if err != nil {
					done <- err
					return
				}
				var request map[string]json.RawMessage
				_ = json.Unmarshal(raw, &request)
				var source, method string
				var params map[string]string
				_ = json.Unmarshal(request["sourceClientId"], &source)
				_ = json.Unmarshal(request["method"], &method)
				_ = json.Unmarshal(request["params"], &params)
				if source != "initializing-client" || method != "initialize" || params["clientType"] != "external-agent" {
					done <- os.ErrInvalid
					return
				}
				response := ownerIPCResponse(request)
				response["handledByClientId"], response["result"] = "external-2", map[string]string{"clientId": "external-2"}
				if variant == "without_handled_by" {
					delete(response, "handledByClientId")
				}
				if variant == "mismatched_client" {
					response["result"] = map[string]string{"clientId": "external-3"}
				}
				done <- writeCodexOwnerIPCFrame(conn, response)
			}()
			c, err := openCodexOwnerIPC(context.Background(), binding)
			if variant == "mismatched_client" {
				if err == nil {
					c.Close()
					t.Fatal("mismatched initialize identity accepted")
				}
				if fixtureErr := <-done; fixtureErr != nil {
					t.Fatal(fixtureErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if c.clientID != "external-2" {
				t.Fatal("initialize lost server-assigned identity")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCodexOwnerIPCDarwinRejectsUnsafePaths(t *testing.T) {
	for _, scenario := range []string{"relative_home", "missing_home", "directory_permissions", "socket_permissions", "directory_symlink", "socket_regular_file"} {
		t.Run(scenario, func(t *testing.T) {
			binding, listener := ownerIPCTestSocket(t)
			home := binding.Env["CODEX_HOME"]
			parent := filepath.Join(home, "ipc")
			path := filepath.Join(parent, "ipc.sock")
			switch scenario {
			case "relative_home":
				binding.Env["CODEX_HOME"] = "relative"
			case "missing_home":
				delete(binding.Env, "CODEX_HOME")
			case "directory_permissions":
				if err := os.Chmod(parent, 0755); err != nil {
					t.Fatal(err)
				}
			case "socket_permissions":
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			case "directory_symlink":
				if err := os.Rename(parent, parent+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+"-real", parent); err != nil {
					t.Fatal(err)
				}
			case "socket_regular_file":
				_ = listener.Close()
				if err := os.WriteFile(path, []byte("not a socket"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if c, err := openCodexOwnerIPC(context.Background(), binding); err == nil {
				c.Close()
				t.Fatal("unsafe IPC path accepted")
			}
		})
	}
}
