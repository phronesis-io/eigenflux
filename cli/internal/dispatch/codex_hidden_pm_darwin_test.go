package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Exercise Runner.Run and the real Unix socket framing against an isolated
// Desktop-owner fixture. No installed Desktop or network peer is contacted.
func TestCodexMacPMUsesHiddenOwnerAndContentOnlyReceipt(t *testing.T) {
	for _, scenario := range []string{"pm-content", "pm-needs-user", "empty-final", "no-final", "pm-protocol", "missing-items", "visible-user-message", "failed", "unsupported", "wrong-completion"} {
		t.Run(scenario, func(t *testing.T) {
			target, fixture := newNoticeOwnerFixture(t, scenario, true)
			b := target.Binding
			b.MainSessionID = noticeSession
			dir := filepath.Join(b.Env["CODEX_HOME"], "sessions")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "rollout-"+noticeSession+".jsonl"), []byte(`{"type":"session_meta","payload":{"id":"`+noticeSession+`"}}`+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := (Runner{Binding: b}).Run(ctx, Request{ID: "fixed-request", Kind: "pm_push", SessionID: noticeSession, Prompt: noticeCodexRules + "\n## Output Contract\nReturn JSON." + codexPMDataMarker + codexMacPMData})
			want := map[string]string{"pm-content": "reply", "pm-needs-user": "needs_user", "empty-final": "no_reply", "no-final": "no_reply"}[scenario]
			if want == "" {
				if err == nil {
					t.Fatalf("unverified result accepted: %+v", result)
				}
			} else {
				d, parseErr := ParseDecision(result.Text, "fixed-request")
				if err != nil || parseErr != nil || d.Action != want || result.SessionID != noticeSession || result.RunID != noticeNewTurn {
					t.Fatalf("hidden decision/receipt: %+v %v %v", result, err, parseErr)
				}
				if want == "reply" && d.ReplyText != "收到测试消息" || want != "reply" && d.ReplyText != "" {
					t.Fatal("owner/protocol text entered reply")
				}
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
			if scenario == "unsupported" {
				wantStarts = 0
			}
			if starts != wantStarts {
				t.Fatalf("unexpected replay/fallback: %d starts", starts)
			}
		})
	}
}
