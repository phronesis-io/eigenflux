package dispatch

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type ownerIPCFragmentReader struct{ io.Reader }

func (r ownerIPCFragmentReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

type ownerIPCFragmentWriter struct{ bytes.Buffer }

func (w *ownerIPCFragmentWriter) Write(p []byte) (int, error) {
	if len(p) > 2 {
		p = p[:2]
	}
	return w.Buffer.Write(p)
}

type ownerIPCEmptyWriter struct{}

func (ownerIPCEmptyWriter) Write([]byte) (int, error) { return 0, nil }

func TestCodexOwnerIPCFraming(t *testing.T) {
	value := map[string]string{"value": "中文\n\"quoted\""}
	writer := &ownerIPCFragmentWriter{}
	if err := writeCodexOwnerIPCFrame(writer, value); err != nil {
		t.Fatal(err)
	}
	raw, err := readCodexOwnerIPCFrame(ownerIPCFragmentReader{bytes.NewReader(writer.Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if json.Unmarshal(raw, &got) != nil || got["value"] != value["value"] {
		t.Fatalf("fragmented frame corrupted: %s", raw)
	}
	if writeCodexOwnerIPCFrame(ownerIPCEmptyWriter{}, value) == nil {
		t.Fatal("zero progress writer accepted")
	}
	if writeCodexOwnerIPCFrame(io.Discard, map[string]string{"large": strings.Repeat("a", codexOwnerIPCMaxFrame)}) == nil {
		t.Fatal("oversized outbound accepted")
	}
	for _, scenario := range []string{"zero", "oversize", "partial_header", "partial_body", "invalid_utf8", "invalid_json"} {
		t.Run(scenario, func(t *testing.T) {
			var frame bytes.Buffer
			var h [4]byte
			data := []byte(`{}`)
			switch scenario {
			case "zero":
				data = nil
			case "oversize":
				binary.LittleEndian.PutUint32(h[:], codexOwnerIPCMaxFrame+1)
				frame.Write(h[:])
			case "partial_header":
				frame.Write([]byte{1, 2})
			case "partial_body":
				binary.LittleEndian.PutUint32(h[:], 5)
				frame.Write(h[:])
				frame.Write(data)
			case "invalid_utf8":
				data = []byte{'"', 0xff, '"'}
			case "invalid_json":
				data = []byte(`{`)
			}
			if scenario == "zero" || scenario == "invalid_utf8" || scenario == "invalid_json" {
				binary.LittleEndian.PutUint32(h[:], uint32(len(data)))
				frame.Write(h[:])
				frame.Write(data)
			}
			if _, err := readCodexOwnerIPCFrame(&frame); err == nil {
				t.Fatal("invalid frame accepted")
			}
		})
	}
}

func ownerIPCResponse(request map[string]json.RawMessage) map[string]any {
	var id, method string
	_ = json.Unmarshal(request["requestId"], &id)
	_ = json.Unmarshal(request["method"], &method)
	return map[string]any{"type": "response", "requestId": id, "method": method, "resultType": "success", "handledByClientId": "owner-1", "result": map[string]bool{"supportsUntrustedAppInput": true}}
}

func TestCodexOwnerIPCCallMatchesOwnerAndRejectsForgedReceipts(t *testing.T) {
	for _, scenario := range []string{"success", "wrong_id", "wrong_owner", "wrong_source", "wrong_method", "remote_error", "duplicate_key"} {
		t.Run(scenario, func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			c := newCodexOwnerIPC(client)
			c.clientID = "external-1"
			defer c.Close()
			done := make(chan error, 1)
			go func() {
				raw, err := readCodexOwnerIPCFrame(server)
				if err != nil {
					done <- err
					return
				}
				var request map[string]json.RawMessage
				_ = json.Unmarshal(raw, &request)
				var source, target string
				_ = json.Unmarshal(request["sourceClientId"], &source)
				_ = json.Unmarshal(request["targetClientId"], &target)
				if source != "external-1" || target != "owner-1" {
					done <- errors.New("unbound request identity")
					return
				}
				response := ownerIPCResponse(request)
				if scenario == "success" {
					if err := writeCodexOwnerIPCFrame(server, map[string]any{"type": "broadcast", "method": "client-status-changed", "params": map[string]string{"secret": "not returned"}}); err != nil {
						done <- err
						return
					}
					if err := writeCodexOwnerIPCFrame(server, map[string]any{"type": "client-discovery-request", "requestId": "discovery-1", "method": "thread-owner-discovery"}); err != nil {
						done <- err
						return
					}
					reply, err := readCodexOwnerIPCFrame(server)
					if err != nil {
						done <- err
						return
					}
					var declined struct {
						Type      string `json:"type"`
						RequestID string `json:"requestId"`
						Response  struct {
							CanHandle bool `json:"canHandle"`
						} `json:"response"`
					}
					if json.Unmarshal(reply, &declined) != nil || declined.Type != "client-discovery-response" || declined.RequestID != "discovery-1" || declined.Response.CanHandle {
						done <- errors.New("client claimed ownership")
						return
					}
				}
				switch scenario {
				case "wrong_id":
					response["requestId"] = "other"
				case "wrong_owner":
					response["handledByClientId"] = "owner-2"
				case "wrong_source":
					response["sourceClientId"] = "owner-2"
				case "wrong_method":
					response["method"] = "other-method"
				case "remote_error":
					response["resultType"], response["error"] = "error", "PRIVATE PAYLOAD TOKEN"
				}
				if scenario == "duplicate_key" {
					encoded, _ := json.Marshal(response)
					encoded = append(encoded[:len(encoded)-1], []byte(`,"resultType":"success"}`)...)
					var header [4]byte
					binary.LittleEndian.PutUint32(header[:], uint32(len(encoded)))
					_, err = server.Write(append(header[:], encoded...))
					done <- err
					return
				}
				done <- writeCodexOwnerIPCFrame(server, response)
			}()
			result, owner, err := c.call(context.Background(), "thread-owner-discovery", 1, map[string]string{"hostId": "local", "conversationId": "thread-1"}, "owner-1")
			if scenario == "success" {
				if err != nil || owner != "owner-1" || !bytes.Contains(result, []byte("supportsUntrustedAppInput")) {
					t.Fatalf("valid response lost: %s %s %v", result, owner, err)
				}
			} else if err == nil || len(result) != 0 || owner != "" || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("forged or private error escaped: %s %s %v", result, owner, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCodexOwnerIPCCallCancellationDoesNotResend(t *testing.T) {
	for _, blocked := range []string{"write", "read", "broadcasts"} {
		t.Run(blocked, func(t *testing.T) {
			client, server := net.Pipe()
			c := newCodexOwnerIPC(client)
			defer c.Close()
			defer server.Close()
			requests := make(chan int, 1)
			go func() {
				if blocked == "write" {
					<-time.After(100 * time.Millisecond)
					requests <- 0
					return
				}
				if _, err := readCodexOwnerIPCFrame(server); err != nil {
					requests <- 0
					return
				}
				if blocked == "broadcasts" {
					for writeCodexOwnerIPCFrame(server, map[string]string{"type": "broadcast", "method": "activity"}) == nil {
					}
				}
				requests <- 1
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, _, err := c.call(ctx, "thread-owner-discovery", 1, map[string]string{}, "")
			if err == nil || time.Since(start) > time.Second {
				t.Fatalf("cancellation did not bound call: %v", err)
			}
			_ = c.Close()
			if got := <-requests; got > 1 {
				t.Fatal("request replayed")
			}
		})
	}
}
