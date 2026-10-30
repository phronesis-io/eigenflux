//go:build windows

package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// These fixtures never create or connect to the Desktop's codex-ipc pipe.
func ownerIPCWindowsFixture(t *testing.T) (string, net.Listener) {
	t.Helper()
	id, err := codexOwnerIPCRequestID()
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user.User.Sid == nil {
		t.Fatal("cannot identify fixture user")
	}
	path := `\\.\pipe\eigenflux-owner-fixture-` + id
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + user.User.Sid.String() + ")",
		InputBufferSize:    4096,
		OutputBufferSize:   4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return path, listener
}

func ownerIPCWindowsPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	path, listener := ownerIPCWindowsFixture(t)
	type accepted struct {
		conn net.Conn
		err  error
	}
	ready := make(chan accepted, 1)
	go func() {
		conn, err := listener.Accept()
		ready <- accepted{conn, err}
	}()
	client, err := dialCodexOwnerWindowsPipe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	select {
	case value := <-ready:
		if value.err != nil {
			t.Fatal(value.err)
		}
		t.Cleanup(func() { _ = value.conn.Close() })
		return client, value.conn
	case <-time.After(5 * time.Second):
		t.Fatal("isolated pipe did not accept its client")
		return nil, nil
	}
}

func TestCodexOwnerWindowsSameUserPeerAndFramedInitialize(t *testing.T) {
	conn, server := ownerIPCWindowsPair(t)
	done := make(chan error, 1)
	go func() {
		raw, err := readCodexOwnerIPCFrame(server)
		if err != nil {
			done <- err
			return
		}
		var request map[string]json.RawMessage
		if err := json.Unmarshal(raw, &request); err != nil {
			done <- err
			return
		}
		var method, source string
		var version int
		var params map[string]string
		if json.Unmarshal(request["method"], &method) != nil || json.Unmarshal(request["sourceClientId"], &source) != nil ||
			json.Unmarshal(request["version"], &version) != nil || json.Unmarshal(request["params"], &params) != nil ||
			method != "initialize" || source != "initializing-client" || version != 0 || params["clientType"] != "external-agent" {
			done <- fmt.Errorf("unexpected isolated initialize envelope")
			return
		}
		response := ownerIPCResponse(request)
		response["handledByClientId"] = "fixture-external-1"
		response["result"] = map[string]string{"clientId": "fixture-external-1"}
		done <- writeCodexOwnerIPCFrame(server, response)
	}()
	client := newCodexOwnerIPC(conn)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, handledBy, err := client.call(ctx, "initialize", 0, map[string]string{"clientType": "external-agent"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ClientID string `json:"clientId"`
	}
	if json.Unmarshal(raw, &result) != nil || result.ClientID != "fixture-external-1" || handledBy != result.ClientID {
		t.Fatal("isolated initialize lost the server-assigned identity")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCodexOwnerWindowsCanceledDial(t *testing.T) {
	path, _ := ownerIPCWindowsFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn, err := dialCodexOwnerWindowsPipe(ctx, path)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("canceled dial returned a connection")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dial lost its context error: %v", err)
	}
}

func TestCodexOwnerWindowsPipeReadDeadline(t *testing.T) {
	conn, _ := ownerIPCWindowsPair(t)
	if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var value [1]byte
	_, err := conn.Read(value[:])
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("named-pipe read did not enforce its deadline: %v", err)
	}
}

func TestCodexOwnerWindowsRequestCancellationClosesPipe(t *testing.T) {
	for _, scenario := range []string{"deadline", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			conn, server := ownerIPCWindowsPair(t)
			requested, closed := make(chan error, 1), make(chan error, 1)
			go func() {
				_, err := readCodexOwnerIPCFrame(server)
				requested <- err
				if err != nil {
					closed <- err
					return
				}
				var value [1]byte
				_, err = server.Read(value[:])
				closed <- err
			}()
			client := newCodexOwnerIPC(conn)
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, err := client.call(ctx, "thread-owner-discovery", 1, map[string]string{"hostId": "local", "conversationId": "isolated-fixture-thread"}, "")
				done <- err
			}()
			select {
			case err := <-requested:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("isolated request was not received")
			}
			if scenario == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || scenario == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("uncertain request was accepted after cancellation: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("canceled request remained blocked")
			}
			select {
			case err := <-closed:
				if err == nil {
					t.Fatal("fixture did not observe connection closure")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("canceled request left its named pipe open")
			}
		})
	}
}

func TestCodexOwnerWindowsRejectsInvalidHomeBeforeDial(t *testing.T) {
	// Every value fails preflight. A canceled context also prevents a real-pipe
	// connection if a future regression accidentally reaches the dial helper.
	for _, home := range []string{"", "relative", `C:relative`, "C:\\fixture\x00"} {
		t.Run(fmt.Sprintf("home=%q", home), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			conn, err := dialCodexOwnerIPC(ctx, Binding{Host: "codex", Mode: "native", Env: map[string]string{"CODEX_HOME": home}})
			if conn != nil {
				_ = conn.Close()
				t.Fatal("invalid home passed the pre-dial check")
			}
			if err == nil || err.Error() != "main_notice_owner_home_invalid" {
				t.Fatalf("invalid home was not rejected before dial: %v", err)
			}
		})
	}
}

func TestCodexOwnerWindowsRejectsUnverifiablePeer(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if err := verifyCodexOwnerWindowsPeer(left); err == nil {
		t.Fatal("connection without a Windows pipe handle was trusted")
	}
}
