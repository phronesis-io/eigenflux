package dispatch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// This fake is a stateful RPC peer which validates client capabilities and handles replay.
func fakeACP(scenario string) {
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if encoder.Encode(v) != nil {
			os.Exit(2)
		}
	}
	update := func(session, kind, text string) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": session, "update": map[string]any{"sessionUpdate": kind, "content": map[string]string{"type": "text", "text": text}}}})
	}
	session := "new-session"
	for {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if decoder.Decode(&m) != nil {
			return
		}
		response := func(result any) { send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}) }
		switch m.Method {
		case "initialize":
			var p struct {
				ProtocolVersion int            `json:"protocolVersion"`
				Capabilities    map[string]any `json:"clientCapabilities"`
			}
			if json.Unmarshal(m.Params, &p) != nil || p.ProtocolVersion != 1 || len(p.Capabilities) != 0 {
				os.Exit(3)
			}
			response(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]bool{"loadSession": scenario != "acp-no-load"}})
		case "session/new":
			response(map[string]string{"sessionId": session})
		case "session/load":
			session = "old-session"
			update(session, "agent_message_chunk", "HISTORY MUST NOT LEAK")
			response(map[string]any{})
		case "session/prompt":
			switch scenario {
			case "acp-timeout":
				time.Sleep(20 * time.Second)
				return
			case "acp-exit":
				fmt.Fprint(os.Stderr, "secret token")
				os.Exit(8)
			case "acp-aggregate-size":
				for i := 0; i < 5; i++ {
					update(session, "agent_message_chunk", strings.Repeat("x", 300000))
				}
				return
			case "acp-size":
				fmt.Println(strings.Repeat("x", maxRunnerOutput+10))
				return
			case "acp-wrong-id":
				send(map[string]any{"jsonrpc": "2.0", "id": 999, "result": map[string]string{"stopReason": "end_turn"}})
				continue
			case "acp-permission":
				send(map[string]any{"jsonrpc": "2.0", "id": "permission-1", "method": "session/request_permission", "params": map[string]string{"sessionId": session}})
				continue
			case "acp-unknown":
				send(map[string]any{"jsonrpc": "2.0", "id": "tool-1", "method": "fs/read_text_file", "params": map[string]string{"path": "secret"}})
				var answer struct {
					Error struct {
						Code int `json:"code"`
					} `json:"error"`
				}
				if decoder.Decode(&answer) != nil || answer.Error.Code != -32601 {
					os.Exit(4)
				}
			}
			update("different-session", "agent_message_chunk", "WRONG SESSION")
			update(session, "agent_thought_chunk", "PRIVATE THOUGHT")
			update(session, "tool_call", "TOOL LOG")
			update(session, "agent_message_chunk", "current ")
			update(session, "agent_message_chunk", "answer")
			stop := "end_turn"
			if scenario == "acp-incomplete" {
				stop = "max_tokens"
			}
			response(map[string]string{"stopReason": stop})
		case "session/cancel":
			return
		case "":
			if scenario == "acp-permission" {
				if !strings.Contains(string(m.Result), `"cancelled"`) {
					os.Exit(5)
				}
				if path := os.Getenv("EF_PERMISSION_MARKER"); path != "" {
					_ = os.WriteFile(path, []byte("cancelled"), 0600)
				}
			}
		}
	}
}

// A second fake uses fixed wire frames and line-oriented reads, independent of the stateful peer.
func fakeACPScript() {
	scan := bufio.NewScanner(os.Stdin)
	for _, frame := range []string{`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}`, `{"jsonrpc":"2.0","id":2,"result":{"sessionId":"script-session"}}`, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"script-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"second implementation"}}}}`} {
		if !scan.Scan() {
			return
		}
		fmt.Println(frame)
	}
	fmt.Println(`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`)
	scan.Scan()
}
func TestACPCurrentTurnAndIndependentPeers(t *testing.T) {
	for _, scenario := range []string{"acp-success", "acp-no-load", "acp-unknown", "acp-script"} {
		t.Run(scenario, func(t *testing.T) {
			b := fixtureBinding(t, "acp", scenario)
			result, err := (Runner{Binding: b}).Run(context.Background(), Request{Prompt: "prompt", SessionID: "old-session"})
			if err != nil {
				t.Fatal(err)
			}
			want := "current answer"
			session := "old-session"
			if scenario == "acp-no-load" {
				session = "new-session"
			}
			if scenario == "acp-script" {
				want = "second implementation"
				session = "script-session"
			}
			if result.Text != want || result.SessionID != session {
				t.Fatalf("result %#v", result)
			}
		})
	}
}
func TestACPFailureBoundaries(t *testing.T) {
	for _, scenario := range []string{"acp-permission", "acp-timeout", "acp-exit", "acp-size", "acp-aggregate-size", "acp-wrong-id", "acp-incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			b := fixtureBinding(t, "acp", scenario)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			result, err := (Runner{Binding: b}).Run(ctx, Request{Prompt: "hello"})
			if err == nil || result.Text != "" || strings.Contains(err.Error(), "secret") {
				t.Fatalf("result %#v error %v", result, err)
			}
			if scenario == "acp-permission" && !errors.Is(err, ErrNeedsUser) {
				t.Fatal(err)
			}
			if scenario == "acp-timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		})
	}
}

func TestACPPermissionReplyIsCancelled(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := &acpClient{ctx: ctx, stdin: writer, incoming: make(chan acpRead, 1)}
	reply := make(chan map[string]any, 1)
	go func() {
		decoder := json.NewDecoder(reader)
		var request map[string]any
		_ = decoder.Decode(&request)
		client.incoming <- acpRead{message: acpMessage{JSONRPC: "2.0", ID: json.RawMessage(`"permission-id"`), Method: "session/request_permission"}}
		var response map[string]any
		_ = decoder.Decode(&response)
		reply <- response
	}()
	_, err := client.call("session/prompt", map[string]any{}, true)
	if !errors.Is(err, ErrNeedsUser) {
		t.Fatal(err)
	}
	response := <-reply
	if response["id"] != "permission-id" {
		t.Fatalf("wrong permission response %#v", response)
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing permission result %#v", response)
	}
	outcome, ok := result["outcome"].(map[string]any)
	if !ok || outcome["outcome"] != "cancelled" {
		t.Fatalf("permission was not cancelled %#v", response)
	}
}
