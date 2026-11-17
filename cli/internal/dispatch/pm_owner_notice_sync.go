package dispatch

import (
	"context"
	"slices"
	"strings"
	"time"
)

const PMNoticeUploadLimit = 4 // First submission plus three retries.

type PMNoticeUpload struct {
	ResultID       string `json:"result_id"`
	ConversationID string `json:"conversation_id"`
	Summary        string `json:"summary"`
	NeedsAction    bool   `json:"needs_action"`
	GeneratedAt    int64  `json:"generated_at"`
	Attempts       int    `json:"attempts,omitempty"`
	NextAttemptAt  int64  `json:"next_attempt_at,omitempty"`
}

func queuePMNoticeUpload(state *pmNoticeState, entry PMNoticeEntry, now time.Time) {
	if slices.ContainsFunc(state.Sync, func(item PMNoticeUpload) bool { return item.ResultID == entry.ID }) {
		return
	}
	if len(state.Sync) >= pmNoticeLimit {
		state.Sync = slices.Delete(state.Sync, 0, 1)
	}
	state.Sync = append(state.Sync, PMNoticeUpload{
		ResultID: entry.ID, ConversationID: entry.Conversation, Summary: strings.TrimSpace(entry.Notice.Summary),
		NeedsAction: entry.Notice.NeedsAction, GeneratedAt: now.UnixMilli(),
	})
}

// Claim records the attempt before issuing a request, so an interrupted Watch
// cannot exceed the retry limit after restarting.
func ClaimPMNoticeUpload(ctx context.Context, b Binding, now time.Time) (PMNoticeUpload, error) {
	var claimed PMNoticeUpload
	err := withPMNotices(ctx, b, func(state *pmNoticeState) (bool, error) {
		size := len(state.Sync)
		state.Sync = slices.DeleteFunc(state.Sync, func(item PMNoticeUpload) bool { return item.Attempts >= PMNoticeUploadLimit })
		for i := range state.Sync {
			item := &state.Sync[i]
			if item.NextAttemptAt > now.UnixMilli() {
				continue
			}
			item.Attempts++
			item.NextAttemptAt = now.Add(time.Minute).UnixMilli()
			claimed = *item
			return true, nil
		}
		return len(state.Sync) != size, nil
	})
	return claimed, err
}

func FinishPMNoticeUpload(ctx context.Context, b Binding, item PMNoticeUpload) error {
	return withPMNotices(ctx, b, func(state *pmNoticeState) (bool, error) {
		size := len(state.Sync)
		state.Sync = slices.DeleteFunc(state.Sync, func(current PMNoticeUpload) bool {
			return current.ResultID == item.ResultID && current.Attempts == item.Attempts
		})
		return len(state.Sync) != size, nil
	})
}
