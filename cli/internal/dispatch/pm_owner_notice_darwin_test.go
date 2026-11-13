package dispatch

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCodexPMOwnerNoticeUsesHiddenOwnerReceipt(t *testing.T) {
	for _, scenario := range []string{"pm-content", "empty-final", "visible-user-message", "failed", "unsupported", "wrong-completion"} {
		t.Run(scenario, func(t *testing.T) {
			target, fixture := newNoticeOwnerFixture(t, scenario, true)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := (MainNoticeRunner{Target: target}).Run(ctx, noticeCodexRules+PMOwnerNoticeMarker+pmNoticeTestData)
			if scenario == "pm-content" {
				if err != nil || result.Text != "收到测试消息" || result.SessionID != noticeSession || result.RunID != noticeNewTurn {
					t.Fatalf("hidden receipt: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatalf("unverified receipt accepted: %+v", result)
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
				t.Fatalf("unexpected replay: %d starts", starts)
			}
		})
	}
}
