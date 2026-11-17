package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCodexPMOwnerNoticeUsesHiddenOwnerReceipt(t *testing.T) {
	for _, scenario := range []string{"pm-content", "empty-final", "visible-user-message", "failed", "unsupported", "wrong-completion", "checkpoint-failed", "checkpoint-cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			target, fixture := newNoticeOwnerFixture(t, scenario, true)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			runner := MainNoticeRunner{Target: target}
			checkpointErr := errors.New("checkpoint write interrupted")
			checkpoints := 0
			runner.BeforeSubmit = func() error {
				checkpoints++
				if scenario == "checkpoint-failed" {
					return checkpointErr
				}
				if scenario == "checkpoint-cancelled" {
					cancel()
				}
				return nil
			}
			result, err := runner.Run(ctx, noticeCodexRules+PMOwnerNoticeMarker+pmNoticeTestData)
			if scenario == "checkpoint-failed" && !errors.Is(err, checkpointErr) {
				t.Fatalf("missing pre-submission checkpoint error: %v", err)
			}
			if scenario == "checkpoint-cancelled" && (!errors.Is(err, context.Canceled) || !MainNoticeCanRetry(err)) {
				t.Fatalf("cancellation before the first write was not retryable: %v", err)
			}
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
			wantCheckpoints := wantStarts
			if scenario == "checkpoint-failed" || scenario == "checkpoint-cancelled" {
				wantStarts = 0
			}
			if checkpoints != wantCheckpoints {
				t.Fatalf("unexpected checkpoint count: %d want %d", checkpoints, wantCheckpoints)
			}
			if starts != wantStarts {
				t.Fatalf("unexpected replay: %d starts", starts)
			}
		})
	}
}
