package mainbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const fixtureSession = "eigenflux-hermes-fixture-main"
const fixturePrompt = "LOCAL FIXTURE: return seven; no real order or account exists."

type hermesFixture struct {
	mu      sync.Mutex
	methods []string
	submits []map[string]any
	config  HermesConfig
	server  *httptest.Server
}

func newHermesFixture(t *testing.T, scenario string) *hermesFixture {
	t.Helper()
	f := &hermesFixture{config: HermesConfig{SessionID: fixtureSession, Cwd: t.TempDir()}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ws" {
			t.Errorf("unexpected endpoint %q", r.URL.Path)
			return
		}
		if scenario == "token" && r.URL.Query().Get("token") != "fixture-local-token" {
			t.Error("explicit token missing")
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		event := func(session, kind string, payload any) {
			_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "method": "event", "params": map[string]any{"type": kind, "session_id": session, "payload": payload}})
		}
		event("", "gateway.ready", map[string]any{"heartbeat": true, "session_authority": scenario == "authority"})
		activated := 0
		for {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if conn.ReadJSON(&request) != nil {
				return
			}
			f.mu.Lock()
			f.methods = append(f.methods, request.Method)
			f.mu.Unlock()
			reply := func(result any) {
				_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			}
			switch request.Method {
			case "session.active_list":
				status := "idle"
				if scenario == "busy" {
					status = "working"
				}
				id := fixtureSession
				if scenario == "missing-session" {
					id = "different-existing-session"
				}
				rows := []any{map[string]any{"id": id, "status": status, "preview": "PRIVATE_FIXTURE_BODY_NOT_FOR_OUTPUT"}}
				if scenario == "duplicate-session" {
					rows = append(rows, rows[0])
				}
				reply(map[string]any{"sessions": rows})
			case "session.activate":
				var params map[string]any
				_ = json.Unmarshal(request.Params, &params)
				if params["session_id"] != fixtureSession || params["omit_messages"] != true || len(params) != 2 {
					t.Errorf("unsafe activate params: %v", params)
					return
				}
				activated++
				id, cwd, status, contract, version := fixtureSession, f.config.Cwd, "idle", 8, "fixture-build-065ea31a"
				running := false
				if scenario == "busy" {
					status = "working"
					running = true
				}
				if scenario == "wrong-session" {
					id = "different-existing-session"
				}
				if scenario == "wrong-cwd" {
					cwd = t.TempDir()
				}
				if scenario == "old-contract" {
					contract = 7
				}
				if scenario == "future-contract" {
					contract = 9
				}
				if scenario == "version-changed" && activated > 1 {
					version = "different-fixture-build"
				}
				payload := map[string]any{"session_id": id, "status": status, "running": running, "messages_omitted": true, "messages": []any{}, "info": map[string]any{"cwd": cwd, "desktop_contract": contract, "version": version}}
				if scenario == "missing-running" {
					delete(payload, "running")
				}
				if scenario == "history-returned" {
					payload["messages"] = []any{map[string]string{"text": "PRIVATE_FIXTURE_BODY_NOT_FOR_OUTPUT"}}
				}
				reply(payload)
			case "prompt.submit":
				var params map[string]any
				_ = json.Unmarshal(request.Params, &params)
				f.mu.Lock()
				f.submits = append(f.submits, params)
				f.mu.Unlock()
				if params["session_id"] != fixtureSession || params["text"] != fixturePrompt || params["display_kind"] != "hidden" || params["queued"] != true || len(params) != 4 {
					t.Errorf("unsafe submit params: %v", params)
					return
				}
				if scenario == "rpc-error" {
					_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": 5070, "message": "PRIVATE_FIXTURE_BODY_NOT_FOR_OUTPUT"}})
					continue
				}
				if scenario == "queued" {
					reply(map[string]any{"status": "queued", "admission_id": "fixture-admission"})
					continue
				}
				if scenario == "missing-user-row" {
					reply(map[string]any{"status": "streaming"})
					continue
				}
				receipt := map[string]any{"complete": true, "user_row_id": 101, "final_assistant_row_id": 105, "row_ids": []int{101, 102, 105}}
				completed := map[string]any{"text": "fixture result: seven", "status": "complete", "persisted_turn": receipt}
				switch scenario {
				case "wrong-user-row":
					receipt["user_row_id"] = 999
				case "incomplete-receipt":
					receipt["complete"] = false
				case "missing-final-row":
					delete(receipt, "final_assistant_row_id")
				case "wrong-row-set":
					receipt["row_ids"] = []int{101, 102}
				case "error-complete":
					completed["status"] = "error"
				case "missing-receipt":
					delete(completed, "persisted_turn")
				}
				if scenario == "early-complete" {
					event(fixtureSession, "message.complete", completed)
				}
				reply(map[string]any{"status": "streaming", "user_row_id": 101})
				if scenario == "timeout" {
					continue
				}
				if scenario == "foreign-complete" {
					event("another-fixture-session", "message.complete", map[string]any{"text": "PRIVATE_FIXTURE_BODY_NOT_FOR_OUTPUT", "status": "complete", "persisted_turn": receipt})
				}
				if scenario == "approval" {
					_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": "fixture-srq", "method": "approval.request", "params": map[string]any{"session_id": fixtureSession}})
					continue
				}
				if scenario != "early-complete" {
					event(fixtureSession, "message.complete", completed)
				}
				if strings.HasPrefix(scenario, "wrong-user") || scenario == "incomplete-receipt" || scenario == "missing-final-row" || scenario == "wrong-row-set" || scenario == "error-complete" || scenario == "missing-receipt" {
					return
				}
			default:
				t.Errorf("forbidden host method %q", request.Method)
				return
			}
		}
	}))
	f.config.URL = "ws" + strings.TrimPrefix(f.server.URL, "http") + "/api/ws"
	if scenario == "token" {
		f.config.TokenFile = filepath.Join(t.TempDir(), "explicit-token")
		if err := os.WriteFile(f.config.TokenFile, []byte("fixture-local-token\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(f.server.Close)
	return f
}

func fixtureInput(t *testing.T, config HermesConfig, operation string) []byte {
	t.Helper()
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	params := map[string]string{"sessionId": config.SessionID, "cwd": config.Cwd}
	if operation == "_eigenflux/main/notify" {
		params["requestId"] = "fixture-request-1"
		params["prompt"] = fixturePrompt
	}
	_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": operation, "params": params})
	return input.Bytes()
}

func fixtureRun(t *testing.T, f *hermesFixture, operation string, timeout time.Duration) ([]rpcFrame, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var output bytes.Buffer
	if err := ServeHermes(ctx, bytes.NewReader(fixtureInput(t, f.config, operation)), &output, f.config); err != nil {
		t.Fatal(err)
	}
	var responses []rpcFrame
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	for decoder.More() {
		var response rpcFrame
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	if strings.Contains(output.String(), "PRIVATE_FIXTURE_BODY_NOT_FOR_OUTPUT") || strings.Contains(output.String(), fixturePrompt) || strings.Contains(output.String(), "fixture-local-token") {
		t.Fatal("private host input leaked into ACP output")
	}
	return responses, output.String()
}

func TestHermesBridgeHiddenReceipt(t *testing.T) {
	for _, scenario := range []string{"success", "early-complete", "foreign-complete", "token"} {
		t.Run(scenario, func(t *testing.T) {
			f := newHermesFixture(t, scenario)
			responses, output := fixtureRun(t, f, "_eigenflux/main/notify", 3*time.Second)
			if len(responses) != 2 || len(responses[1].Error) > 0 {
				t.Fatalf("unexpected responses: %s", output)
			}
			var receipt struct {
				SessionID, RequestID, RunID, StopReason, Text string
				ExistingSession, HiddenInput, ResultOnly      bool
			}
			if json.Unmarshal(responses[1].Result, &receipt) != nil || receipt.SessionID != fixtureSession || receipt.RequestID != "fixture-request-1" || receipt.RunID != "hermes-row-101-105" || receipt.StopReason != "end_turn" || receipt.Text != "fixture result: seven" || !receipt.ExistingSession || !receipt.HiddenInput || !receipt.ResultOnly {
				t.Fatalf("invalid receipt: %s", responses[1].Result)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.submits) != 1 {
				t.Fatalf("submit count %d", len(f.submits))
			}
		})
	}
}

func TestHermesBridgeProbeDoesNotSubmit(t *testing.T) {
	f := newHermesFixture(t, "success")
	responses, output := fixtureRun(t, f, "_eigenflux/main/inspect", 3*time.Second)
	if len(responses) != 2 || len(responses[1].Error) > 0 || !strings.Contains(string(responses[1].Result), `"state":"idle"`) {
		t.Fatalf("invalid inspect: %s", output)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !reflect.DeepEqual(f.methods, []string{"session.active_list", "session.activate", "session.active_list", "session.activate"}) || len(f.submits) != 0 {
		t.Fatalf("unexpected methods: %v", f.methods)
	}
}

func TestHermesBridgeRejectsUnverifiedTarget(t *testing.T) {
	for _, scenario := range []string{"authority", "missing-session", "duplicate-session", "wrong-session", "wrong-cwd", "old-contract", "future-contract", "missing-running", "history-returned", "version-changed", "busy"} {
		t.Run(scenario, func(t *testing.T) {
			f := newHermesFixture(t, scenario)
			responses, output := fixtureRun(t, f, "_eigenflux/main/notify", 3*time.Second)
			if len(responses) == 0 || len(responses[len(responses)-1].Error) == 0 {
				t.Fatalf("target accepted: %s", output)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.submits) != 0 {
				t.Fatal("invalid/busy target received a turn")
			}
		})
	}
}

func TestHermesBridgeDoesNotReplayUnconfirmedTurn(t *testing.T) {
	for _, scenario := range []string{"queued", "missing-user-row", "wrong-user-row", "incomplete-receipt", "missing-final-row", "wrong-row-set", "missing-receipt", "error-complete", "rpc-error", "timeout", "approval"} {
		t.Run(scenario, func(t *testing.T) {
			f := newHermesFixture(t, scenario)
			responses, output := fixtureRun(t, f, "_eigenflux/main/notify", 250*time.Millisecond)
			if len(responses) != 2 || len(responses[1].Error) == 0 {
				t.Fatalf("unconfirmed turn accepted: %s", output)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.submits) != 1 {
				t.Fatalf("submit count %d", len(f.submits))
			}
			for _, method := range f.methods {
				if method != "session.active_list" && method != "session.activate" && method != "prompt.submit" {
					t.Fatalf("fallback or cancellation %q", method)
				}
			}
		})
	}
}

func TestHermesBridgeTargetMismatchDoesNotSubmit(t *testing.T) {
	f := newHermesFixture(t, "success")
	input := fixtureInput(t, f.config, "_eigenflux/main/notify")
	input = bytes.Replace(input, []byte(`"sessionId":"`+fixtureSession+`"`), []byte(`"sessionId":"different-fixture-main"`), 1)
	var output bytes.Buffer
	if err := ServeHermes(context.Background(), bytes.NewReader(input), &output, f.config); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "hermes_main_target_mismatch") {
		t.Fatal(output.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.submits) != 0 {
		t.Fatal("wrong target submitted")
	}
}

func TestHermesBridgeConfigIsExplicitAndLocal(t *testing.T) {
	valid := HermesConfig{URL: "ws://127.0.0.1:9000/api/ws", SessionID: fixtureSession, Cwd: t.TempDir()}
	for name, change := range map[string]func(*HermesConfig){
		"remote":         func(c *HermesConfig) { c.URL = "wss://example.com:443/api/ws" },
		"cloud-token":    func(c *HermesConfig) { c.URL += "?token=secret" },
		"user-info":      func(c *HermesConfig) { c.URL = "ws://user:secret@127.0.0.1:9000/api/ws" },
		"wrong-endpoint": func(c *HermesConfig) { c.URL = "ws://127.0.0.1:9000/other" },
		"relative-cwd":   func(c *HermesConfig) { c.Cwd = "." },
		"relative-token": func(c *HermesConfig) { c.TokenFile = "token" },
		"empty-session":  func(c *HermesConfig) { c.SessionID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			change(&config)
			if _, err := validateHermesConfig(config); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
	if _, err := validateHermesConfig(valid); err != nil {
		t.Fatal(err)
	}
}
