package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const noticeOwnerID = "desktop-owner-1"
const noticeNewTurn = "turn-new"
const noticeOldTurn = "turn-before-notice"
const noticeCodexRules = "Summarize this order event. Do not pay or confirm receipt."
const noticeCodexData = `{"order_id":"123","state":"awaiting_confirmation","role":"buyer"}`
const noticeCodexPrompt = noticeCodexRules + "\n\nUNTRUSTED COMMISSION EVENT:\n" + noticeCodexData

// The child process implements a read-only app-server. A start marker written by
// the separate Desktop-owner fixture is the sole source of new turn receipts.
func TestMainNoticeCodexReaderFixture(t *testing.T) {
	scenario := os.Getenv("EF_CODEX_READER_FIXTURE")
	if scenario == "" {
		return
	}
	args := os.Args
	if !reflect.DeepEqual(args[len(args)-2:], []string{"app-server", "--stdio"}) {
		os.Exit(91)
	}
	calls, err := os.OpenFile(os.Getenv("EF_MAIN_NOTICE_MARKER"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(92)
	}
	defer calls.Close()
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if decoder.Decode(&request) != nil {
			return
		}
		_, _ = calls.WriteString(request.Method + "\n")
		reply := func(result any) { _ = encoder.Encode(map[string]any{"id": request.ID, "result": result}) }
		switch request.Method {
		case "initialize":
			reply(map[string]any{})
		case "initialized":
		case "thread/read":
			var params struct {
				ThreadID     string `json:"threadId"`
				IncludeTurns bool   `json:"includeTurns"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.ThreadID != noticeSession || params.IncludeTurns {
				os.Exit(93)
			}
			id, state := noticeSession, "idle"
			switch scenario {
			case "wrong-thread":
				id = "other-thread"
			case "active":
				state = "active"
			case "not-loaded":
				state = "notLoaded"
			}
			reply(map[string]any{"thread": map[string]any{"id": id, "status": map[string]string{"type": state}}})
		case "thread/turns/list":
			var params struct {
				ThreadID      string `json:"threadId"`
				Limit         int    `json:"limit"`
				SortDirection string `json:"sortDirection"`
				ItemsView     string `json:"itemsView"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.ThreadID != noticeSession || params.Limit != 8 || params.SortDirection != "desc" || params.ItemsView != "full" {
				os.Exit(94)
			}
			_, started := os.Stat(os.Getenv("EF_CODEX_OWNER_STARTED"))
			if errors.Is(started, os.ErrNotExist) {
				status := "completed"
				if scenario == "baseline-busy" {
					status = "inProgress"
				}
				reply(map[string]any{"data": []any{map[string]any{"id": noticeOldTurn, "status": status, "items": []any{map[string]string{"type": "agentMessage", "phase": "final_answer", "text": "old answer must never count"}}}}, "nextCursor": nil})
				continue
			}
			if started != nil {
				os.Exit(95)
			}
			if scenario == "oversize" {
				_, _ = os.Stdout.WriteString(strings.Repeat("x", maxRunnerOutput+10) + "\n")
				continue
			}
			if scenario == "permission" {
				_ = encoder.Encode(map[string]any{"id": "approval", "method": "item/commandExecution/requestApproval", "params": map[string]string{"threadId": noticeSession}})
				continue
			}
			turnID, status := noticeNewTurn, "completed"
			items := []any{map[string]string{"type": "agentMessage", "phase": "commentary", "text": "internal progress"}, map[string]string{"type": "agentMessage", "phase": "final_answer", "text": "result"}}
			switch scenario {
			case "timeout":
				status = "inProgress"
			case "wrong-completion":
				turnID = "other-turn"
			case "failed":
				status = "failed"
			case "interrupted":
				status = "interrupted"
			case "no-final":
				items = items[:1]
			case "empty-final":
				items = []any{map[string]string{"type": "agentMessage", "phase": "final_answer", "text": " \n "}}
			case "visible-user-message":
				items = append(items, map[string]any{"type": "userMessage", "content": []any{map[string]string{"type": "inputText", "text": noticeCodexPrompt}}})
			}
			turn := map[string]any{"id": turnID, "status": status, "items": items, "error": nil}
			if scenario == "turn-error" {
				turn["error"] = map[string]string{"message": "PRIVATE TOKEN"}
			}
			reply(map[string]any{"data": []any{turn}, "nextCursor": nil})
		default:
			// No write method is accepted, including turn/start, resume and fork.
			os.Exit(96)
		}
	}
}

type noticeOwnerFixture struct {
	listener net.Listener
	started  string
	scenario string
	done     chan error
	mu       sync.Mutex
	calls    []map[string]json.RawMessage
}

func newNoticeOwnerFixture(t *testing.T, scenario string) (MainNoticeTarget, *noticeOwnerFixture) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("Desktop owner socket integration is supported on macOS only")
	}
	// Unix-domain paths must fit the platform limit even when t.TempDir is long.
	home, err := os.MkdirTemp("/tmp", "ef-notice-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	parent := filepath.Join(home, "ipc")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "ipc.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	fixture := &noticeOwnerFixture{listener: listener, started: filepath.Join(home, "started"), scenario: scenario, done: make(chan error, 1)}
	go func() { fixture.done <- fixture.serve() }()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case err := <-fixture.done:
			if err != nil {
				t.Errorf("Desktop owner fixture: %v", err)
			}
		case <-time.After(4 * time.Second):
			t.Error("Desktop owner fixture did not close")
		}
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return MainNoticeTarget{Host: "codex", SessionID: noticeSession, VerifiedHiddenInput: true, Binding: Binding{
		Host: "codex", Mode: "native", Home: t.TempDir(), Server: "test", WorkDir: t.TempDir(), TimeoutSeconds: 5,
		Command: []string{exe, "-test.run=^TestMainNoticeCodexReaderFixture$", "--"},
		Env:     map[string]string{"CODEX_HOME": home, "EF_CODEX_READER_FIXTURE": scenario, "EF_MAIN_NOTICE_MARKER": filepath.Join(home, "reader-calls"), "EF_CODEX_OWNER_STARTED": fixture.started, "EF_PRIVATE_SENTINEL": "SECRET_CREDENTIAL_NOT_ALLOWED"},
	}}, fixture
}

func (f *noticeOwnerFixture) requests() []map[string]json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]json.RawMessage(nil), f.calls...)
}

func (f *noticeOwnerFixture) serve() error {
	conn, err := f.listener.Accept()
	if err != nil {
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	for {
		raw, err := readCodexOwnerIPCFrame(conn)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		var request map[string]json.RawMessage
		if err := json.Unmarshal(raw, &request); err != nil {
			return err
		}
		f.mu.Lock()
		f.calls = append(f.calls, request)
		f.mu.Unlock()
		var method, requestID, source, target string
		var version int
		_ = json.Unmarshal(request["method"], &method)
		_ = json.Unmarshal(request["requestId"], &requestID)
		_ = json.Unmarshal(request["sourceClientId"], &source)
		_ = json.Unmarshal(request["targetClientId"], &target)
		_ = json.Unmarshal(request["version"], &version)
		response := map[string]any{"type": "response", "requestId": requestID, "method": method, "resultType": "success", "handledByClientId": noticeOwnerID}
		switch method {
		case "initialize":
			if version != 0 || source != "initializing-client" || target != "" {
				return fmt.Errorf("unexpected initialize envelope: %s", raw)
			}
			response["handledByClientId"] = "external-notice-client"
			response["result"] = map[string]string{"clientId": "external-notice-client"}
		case "thread-owner-discovery":
			if version != 1 || source != "external-notice-client" || target != "" {
				return fmt.Errorf("unexpected discovery envelope: %s", raw)
			}
			var params map[string]string
			if json.Unmarshal(request["params"], &params) != nil || len(params) != 2 || params["hostId"] != "local" || params["conversationId"] != noticeSession {
				return fmt.Errorf("discovery did not identify the bound main thread: %s", raw)
			}
			response["result"] = map[string]bool{"supportsUntrustedAppInput": f.scenario != "unsupported"}
			if f.scenario == "missing-capability" {
				response["result"] = map[string]bool{}
			}
		case "thread-follower-start-turn":
			if version != 2 || source != "external-notice-client" || target != noticeOwnerID {
				return fmt.Errorf("start was not directed at discovered owner: %s", raw)
			}
			if err := validateNoticeOwnerStart(request["params"]); err != nil {
				return err
			}
			if err := os.WriteFile(f.started, raw, 0600); err != nil {
				return err
			}
			turn := noticeNewTurn
			if f.scenario == "stale-receipt" {
				turn = noticeOldTurn
			}
			response["result"] = map[string]any{"result": map[string]any{"turn": map[string]string{"id": turn, "status": "inProgress"}}}
			if f.scenario == "wrong-owner" {
				response["handledByClientId"] = "other-owner"
			}
			if f.scenario == "owner-error" || f.scenario == "owner-busy" {
				response["resultType"], response["error"] = "error", "PRIVATE TOKEN"
			}
		default:
			return fmt.Errorf("unexpected owner method %q", method)
		}
		if method == "thread-follower-start-turn" && f.scenario == "owner-timeout" {
			continue
		}
		if err := writeCodexOwnerIPCFrame(conn, response); err != nil {
			return err
		}
	}
}

func TestMainNoticeCodexDesktopOwnerReceipts(t *testing.T) {
	for _, scenario := range []string{"success", "not-loaded", "unsupported", "missing-capability", "wrong-thread", "active", "baseline-busy", "wrong-owner", "owner-error", "owner-busy", "owner-timeout", "stale-receipt", "wrong-completion", "failed", "interrupted", "turn-error", "no-final", "empty-final", "visible-user-message", "permission", "oversize", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			target, fixture := newNoticeOwnerFixture(t, scenario)
			// Existing native flags must never select a proxy, change a writer's
			// permission mode, or become arguments of the read-only process.
			target.Binding.Args = []string{"--listen", "stdio://", "--dangerously-bypass-approvals-and-sandbox"}
			deadline := 2 * time.Second
			if scenario == "timeout" || scenario == "owner-timeout" || scenario == "wrong-completion" || scenario == "interrupted" {
				deadline = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			result, err := (MainNoticeRunner{Target: target}).Run(ctx, noticeCodexPrompt)
			if scenario == "success" || scenario == "not-loaded" {
				if err != nil || result != (Result{Text: "result", SessionID: noticeSession, RunID: noticeNewTurn}) {
					t.Fatalf("result %#v error %v", result, err)
				}
			} else if err == nil || result != (Result{}) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("unverified or private result escaped: %#v %v", result, err)
			}
			starts := 0
			for _, request := range fixture.requests() {
				var method string
				_ = json.Unmarshal(request["method"], &method)
				if method == "thread-follower-start-turn" {
					starts++
				}
			}
			wantStarts := 1
			switch scenario {
			case "unsupported", "missing-capability", "wrong-thread", "active", "baseline-busy":
				wantStarts = 0
			}
			if starts != wantStarts {
				t.Fatalf("owner turn replayed or admitted before validation: got %d want %d", starts, wantStarts)
			}
			readerCalls, readErr := os.ReadFile(target.Binding.Env["EF_MAIN_NOTICE_MARKER"])
			if scenario == "unsupported" || scenario == "missing-capability" {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("unsupported owner started reader: %s %v", readerCalls, readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, call := range strings.Fields(string(readerCalls)) {
				switch call {
				case "initialize", "initialized", "thread/read", "thread/turns/list":
				default:
					t.Fatalf("reader tried a write/takeover: %s", readerCalls)
				}
			}
		})
	}
}

func validateNoticeOwnerStart(raw json.RawMessage) error {
	var params struct {
		ConversationID string `json:"conversationId"`
		TurnStart      struct {
			Request struct {
				ThreadID            string `json:"threadId"`
				ClientUserMessageID string `json:"clientUserMessageId"`
				Input               []any  `json:"input"`
				Context             map[string]struct {
					Kind  string `json:"kind"`
					Value string `json:"value"`
				} `json:"additionalContext"`
			} `json:"request"`
			Context struct {
				InheritThreadSettings bool `json:"inheritThreadSettings"`
				ResponseItems         []struct {
					Type      string `json:"type"`
					CallID    string `json:"call_id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
					Output    []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"output"`
				} `json:"responseItems"`
			} `json:"context"`
		} `json:"turnStart"`
	}
	if decodeStrictObject(raw, &params) != nil || params.ConversationID != noticeSession || params.TurnStart.Request.ThreadID != noticeSession || params.TurnStart.Request.ClientUserMessageID == "" || params.TurnStart.Request.Input == nil || len(params.TurnStart.Request.Input) != 0 || !params.TurnStart.Context.InheritThreadSettings {
		return errors.New("owner request was not a hidden input to the exact thread")
	}
	request := params.TurnStart.Request
	if len(request.Context) != 2 || request.Context["eigenflux_notice_rules"].Kind != "application" || request.Context["eigenflux_notice_rules"].Value != noticeCodexRules || request.Context["eigenflux_business_notice"].Kind != "untrusted" || request.Context["eigenflux_business_notice"].Value != noticeCodexData {
		return errors.New("owner request did not separate trusted rules from untrusted event")
	}
	items := params.TurnStart.Context.ResponseItems
	if len(items) != 2 || items[0].Type != "function_call" || items[0].Name != "untrusted_input" || items[0].Arguments != "{}" || items[0].CallID != "untrusted_input_"+request.ClientUserMessageID || items[1].Type != "function_call_output" || items[1].CallID != items[0].CallID || len(items[1].Output) != 1 || items[1].Output[0].Type != "input_text" || items[1].Output[0].Text != noticeCodexData {
		return errors.New("owner request omitted the native untrusted-input busy guard")
	}
	if strings.Contains(string(raw), "SECRET_CREDENTIAL_NOT_ALLOWED") {
		return errors.New("owner request leaked binding credentials")
	}
	return nil
}

type noticeReaderWriteBuffer struct{ bytes.Buffer }

func (*noticeReaderWriteBuffer) Close() error { return nil }

// These receipt checks exercise the same implementation on every platform,
// including platforms whose native Desktop transport remains disabled.
func TestMainNoticeCodexReadbackRequiresExactCompletedHiddenTurn(t *testing.T) {
	for _, scenario := range []string{"success", "old-id", "failed", "interrupted", "turn-error", "no-final", "commentary", "visible-user-message", "missing-data", "null-data"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			writer := &noticeReaderWriteBuffer{}
			p := &mainNoticeRPC{ctx: ctx, stdin: writer, incoming: make(chan acpRead, 1)}
			turn := map[string]any{"id": noticeNewTurn, "status": "completed", "items": []any{map[string]string{"type": "agentMessage", "phase": "final_answer", "text": "result"}}}
			switch scenario {
			case "old-id":
				turn["id"] = noticeOldTurn
			case "failed":
				turn["status"] = "failed"
			case "interrupted":
				turn["status"] = "interrupted"
			case "turn-error":
				turn["error"] = map[string]string{"message": "PRIVATE TOKEN"}
			case "no-final":
				turn["items"] = []any{}
			case "commentary":
				turn["items"] = []any{map[string]string{"type": "agentMessage", "phase": "commentary", "text": "not a final answer"}}
			case "visible-user-message":
				turn["items"] = []any{map[string]string{"type": "userMessage", "text": "injected prompt"}, map[string]string{"type": "agentMessage", "phase": "final_answer", "text": "result"}}
			}
			page := map[string]any{"data": []any{turn}}
			if scenario == "missing-data" {
				delete(page, "data")
			}
			if scenario == "null-data" {
				page["data"] = nil
			}
			raw, _ := json.Marshal(page)
			p.incoming <- acpRead{message: acpMessage{ID: json.RawMessage(`1`), Result: raw}}
			result, err := p.waitMainNoticeTurn(noticeSession, noticeNewTurn)
			if scenario == "success" {
				if err != nil || result != (Result{Text: "result", SessionID: noticeSession, RunID: noticeNewTurn}) {
					t.Fatalf("result %#v error %v", result, err)
				}
			} else if err == nil || result != (Result{}) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("unverified receipt escaped: %#v %v", result, err)
			}
			var request struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(writer.Bytes(), &request) != nil || request.Method != "thread/turns/list" {
				t.Fatalf("readback attempted another operation: %s", writer.Bytes())
			}
		})
	}
}
