//go:build windows

package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real named-pipe transport plus a read-only child reader; no Desktop, account,
// network message, or model is contacted by this fixture.
func TestCodexHiddenPMWindowsOwnerAndSilentReceipt(t *testing.T) {
	for _, scenario := range []string{"empty-final", "no-final", "missing-items", "visible-user-message", "failed", "wrong-completion", "permission", "success"} {
		t.Run(scenario, func(t *testing.T) {
			client, server := ownerIPCWindowsPair(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			marker := filepath.Join(t.TempDir(), "started")
			done := make(chan error, 1)
			go func() {
				for _, method := range []string{"thread-owner-discovery", "thread-follower-start-turn"} {
					raw, err := readCodexOwnerIPCFrame(server)
					if err != nil {
						done <- err
						return
					}
					var request struct {
						RequestID string                     `json:"requestId"`
						Method    string                     `json:"method"`
						Params    map[string]json.RawMessage `json:"params"`
					}
					if json.Unmarshal(raw, &request) != nil || request.Method != method {
						done <- fmt.Errorf("unexpected owner method")
						return
					}
					var result any = map[string]bool{"supportsUntrustedAppInput": true}
					if method == "thread-follower-start-turn" {
						var start struct {
							Request struct {
								Input    []any  `json:"input"`
								ThreadID string `json:"threadId"`
							} `json:"request"`
							Context struct {
								Inherit bool              `json:"inheritThreadSettings"`
								Items   []json.RawMessage `json:"responseItems"`
							} `json:"context"`
						}
						if json.Unmarshal(request.Params["turnStart"], &start) != nil || start.Request.Input == nil || len(start.Request.Input) != 0 || start.Request.ThreadID != noticeSession || !start.Context.Inherit || len(start.Context.Items) != 2 || !strings.Contains(string(raw), "empty final answer") {
							done <- fmt.Errorf("visible input, wrong thread or missing silence contract")
							return
						}
						if err := os.WriteFile(marker, raw, 0600); err != nil {
							done <- err
							return
						}
						result = map[string]any{"result": map[string]any{"turn": map[string]string{"id": noticeNewTurn, "status": "inProgress"}}}
					}
					if err := writeCodexOwnerIPCFrame(server, map[string]any{"type": "response", "requestId": request.RequestID, "method": method, "resultType": "success", "handledByClientId": noticeOwnerID, "result": result}); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			binding := Binding{Host: "codex", Mode: "native", WorkDir: t.TempDir(), Command: []string{exe, "-test.run=^TestMainNoticeCodexReaderFixture$", "--"}, Env: map[string]string{"EF_CODEX_READER_FIXTURE": scenario, "EF_MAIN_NOTICE_MARKER": filepath.Join(t.TempDir(), "reader"), "EF_CODEX_OWNER_STARTED": marker}}
			runner := MainNoticeRunner{Target: MainNoticeTarget{Host: "codex", SessionID: noticeSession, Binding: binding}}
			req := Request{ID: "fixed-request", SessionID: noticeSession, Prompt: "trusted rules" + codexPMDataMarker + `{"request_id":"fixed-request"}`}
			got, err := (Runner{}).runCodexHiddenPMWith(ctx, req, func(ctx context.Context, rules, data string) (Result, error) {
				return runner.runCodexEnvelopeOnIPC(ctx, newCodexOwnerIPC(client), rules, data, true)
			})
			if scenario == "empty-final" || scenario == "no-final" {
				d, decodeErr := ParseDecision(got.Text, req.ID)
				if err != nil || decodeErr != nil || d.Action != "no_reply" || got.SessionID != noticeSession || got.RunID != noticeNewTurn {
					t.Fatalf("silent receipt lost: %+v %v %v", got, err, decodeErr)
				}
			} else if err == nil {
				t.Fatalf("unconfirmed/visible/permission result accepted: %+v", got)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
