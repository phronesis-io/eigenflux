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

type pmNoticeUploadState struct {
	Pending []PMNoticeUpload `json:"pending"`
	Seen    []string         `json:"seen"`
}

func pmNoticeUploadFile(b Binding) string {
	// Uploads belong to the account on this endpoint across Watch rebindings.
	return "pm-notice-uploads-" + journalID(b.Scope, b.Endpoint) + ".json"
}

// QueuePMNoticeUpload accepts the same checkpoint on replay without resetting
// its attempt count or re-enqueueing a recently completed upload.
func QueuePMNoticeUpload(ctx context.Context, b Binding, job Job, notice PMOwnerNotice) error {
	id, err := pmNoticeResultID(b, job, notice)
	if err != nil {
		return err
	}
	return withPMNoticeFile(ctx, b, pmNoticeUploadFile(b), func(state *pmNoticeUploadState) (bool, error) {
		if slices.Contains(state.Seen, id) ||
			slices.ContainsFunc(state.Pending, func(item PMNoticeUpload) bool { return item.ResultID == id }) {
			return false, nil
		}
		if len(state.Pending) >= pmNoticeLimit {
			state.Pending = slices.Delete(state.Pending, 0, 1)
		}
		state.Pending = append(state.Pending, PMNoticeUpload{
			ResultID: id, ConversationID: job.Message.Conversation, Summary: strings.TrimSpace(notice.Summary),
			NeedsAction: notice.NeedsAction, GeneratedAt: time.Now().UnixMilli(),
		})
		state.Seen = appendPMNoticeReceipt(state.Seen, id)
		return true, nil
	})
}

// Claim records the attempt before issuing a request, so an interrupted Watch
// cannot exceed the retry limit after restarting.
func ClaimPMNoticeUpload(ctx context.Context, b Binding, now time.Time) (PMNoticeUpload, error) {
	var claimed PMNoticeUpload
	err := withPMNoticeFile(ctx, b, pmNoticeUploadFile(b), func(state *pmNoticeUploadState) (bool, error) {
		size := len(state.Pending)
		state.Pending = slices.DeleteFunc(state.Pending, func(item PMNoticeUpload) bool { return item.Attempts >= PMNoticeUploadLimit })
		for i := range state.Pending {
			item := &state.Pending[i]
			if item.NextAttemptAt > now.UnixMilli() {
				continue
			}
			item.Attempts++
			item.NextAttemptAt = now.Add(time.Minute).UnixMilli()
			claimed = *item
			return true, nil
		}
		return len(state.Pending) != size, nil
	})
	return claimed, err
}

func FinishPMNoticeUpload(ctx context.Context, b Binding, item PMNoticeUpload) error {
	return withPMNoticeFile(ctx, b, pmNoticeUploadFile(b), func(state *pmNoticeUploadState) (bool, error) {
		size := len(state.Pending)
		state.Pending = slices.DeleteFunc(state.Pending, func(current PMNoticeUpload) bool {
			return current.ResultID == item.ResultID && current.Attempts == item.Attempts
		})
		return len(state.Pending) != size, nil
	})
}
