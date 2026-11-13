package dispatch

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/filelock"
)

const PMOwnerNoticeMarker = "\n\nUNTRUSTED PRIVATE MESSAGE SUMMARY:\n"

const pmNoticeLimit = 256
const pmNoticeHistoryLimit = 1024
const pmNoticeBatchLimit = 5

// PMOwnerNotice is an independent decision about what the owner should see.
type PMOwnerNotice struct {
	Topic       string `json:"topic"`
	Change      string `json:"change"`
	Summary     string `json:"summary"`
	NeedsAction bool   `json:"needs_action"`
}

var pmNoticeKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

func (n PMOwnerNotice) valid() bool {
	return pmNoticeKey.MatchString(n.Topic) && pmNoticeKey.MatchString(n.Change) &&
		strings.TrimSpace(n.Summary) != "" && utf8.RuneCountInString(n.Summary) <= 300 &&
		!strings.ContainsRune(n.Summary, utf8.RuneError) && strings.IndexFunc(n.Summary, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	}) < 0
}

type PMNoticeEntry struct {
	ID           string        `json:"id"`
	Conversation string        `json:"conversation"`
	Sender       string        `json:"sender,omitempty"`
	Notice       PMOwnerNotice `json:"notice"`
	RetryAt      int64         `json:"retry_at,omitempty"`
	Attempts     int           `json:"attempts,omitempty"`
}

type pmNoticeState struct {
	Pending []PMNoticeEntry `json:"pending"`
	History []PMNoticeEntry `json:"history"`
	Seen    []string        `json:"seen"`
	Cycles  []string        `json:"cycles"`
}

// Watch and heartbeat share this small spool. Hold the retained OS lock only
// across local reads/writes. A claimed item never replays after an uncertain handoff.
func withPMNotices(ctx context.Context, b Binding, change func(*pmNoticeState) (bool, error)) error {
	if err := validJournalBinding(b); err != nil {
		return err
	}
	path := filepath.Join(b.Home, "watch", "pm-notices-"+journalID(b.Scope, b.Revision)+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		if err := deadline.Err(); err != nil {
			return err
		}
		lock, acquired, err := filelock.TryAcquire(path + ".lock")
		if err != nil {
			return err
		}
		if acquired {
			defer lock.Release()
			break
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-deadline.Done():
			timer.Stop()
			return deadline.Err()
		case <-timer.C:
		}
	}
	var state pmNoticeState
	f, err := os.Open(path)
	if err == nil {
		raw, readErr := io.ReadAll(io.LimitReader(f, 4<<20))
		_ = f.Close()
		if readErr != nil {
			return readErr
		}
		if len(raw) == 4<<20 || decodeStrictObject(raw, &state) != nil {
			return errors.New("invalid_pm_notice_state")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	write, err := change(&state)
	if err != nil || !write {
		return err
	}
	return WriteJSON(path, state)
}

func HasPendingPMNotices(ctx context.Context, b Binding) (bool, error) {
	pending := false
	err := withPMNotices(ctx, b, func(state *pmNoticeState) (bool, error) {
		pending = len(state.Pending) > 0
		return false, nil
	})
	return pending, err
}

func RecentPMNotices(ctx context.Context, b Binding, conversation string) ([]PMOwnerNotice, error) {
	recent := []PMOwnerNotice{}
	err := withPMNotices(ctx, b, func(state *pmNoticeState) (bool, error) {
		for _, entries := range [][]PMNoticeEntry{state.Pending, state.History} {
			for i := len(entries) - 1; i >= 0 && len(recent) < 8; i-- {
				if entries[i].Conversation == conversation {
					recent = append(recent, entries[i].Notice)
				}
			}
		}
		return false, nil
	})
	return recent, err
}

func SavePMNotice(ctx context.Context, b Binding, job Job, n PMOwnerNotice) error {
	if job.Kind != "pm_push" || job.Message == nil || job.Scope != b.Scope || job.Revision != b.Revision || !n.valid() {
		return errors.New("invalid_pm_owner_notice")
	}
	n.Summary = strings.TrimSpace(n.Summary)
	return withPMNotices(ctx, b, func(state *pmNoticeState) (bool, error) {
		id := journalID(job.Message.Conversation, n.Topic, n.Change)
		if slices.Contains(state.Seen, id) || slices.ContainsFunc(state.Pending, func(e PMNoticeEntry) bool { return e.ID == id }) {
			return false, nil
		}
		index := slices.IndexFunc(state.Pending, func(e PMNoticeEntry) bool {
			return e.Conversation == job.Message.Conversation && e.Notice.Topic == n.Topic
		})
		if index < 0 && len(state.Pending) >= pmNoticeLimit {
			// Keep the bounded queue available for urgent decisions.
			evict := slices.IndexFunc(state.Pending, func(e PMNoticeEntry) bool { return !e.Notice.NeedsAction })
			if !n.NeedsAction || evict < 0 {
				return false, errors.New("pm_owner_notice_queue_full")
			}
			state.Seen = appendPMNoticeReceipt(state.Seen, state.Pending[evict].ID)
			state.Pending = slices.Delete(state.Pending, evict, evict+1)
		}
		entry := PMNoticeEntry{ID: id, Conversation: job.Message.Conversation, Sender: safePMNoticeSender(job.Message.SenderName), Notice: n}
		if index >= 0 {
			state.Seen = appendPMNoticeReceipt(state.Seen, state.Pending[index].ID)
			state.Pending[index] = entry
		} else {
			state.Pending = append(state.Pending, entry)
		}
		return true, nil
	})
}

func safePMNoticeSender(name string) string {
	name = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)), " ")
	runes := []rune(name)
	if len(runes) > 80 {
		return string(runes[:80])
	}
	return name
}

func appendPMNoticeReceipt(receipts []string, id string) []string {
	receipts = append(receipts, id)
	return receipts[max(0, len(receipts)-pmNoticeHistoryLimit):]
}

// ClaimPMNotices returns one batch at most once for the given heartbeat token.
// Codex claims one item at a time, so a pre-submission retry cannot replay others.
func ClaimPMNotices(ctx context.Context, b Binding, cycle string, now time.Time) ([]PMNoticeEntry, error) {
	var claimed []PMNoticeEntry
	err := withPMNotices(ctx, b, func(state *pmNoticeState) (bool, error) {
		if cycle != "" && slices.Contains(state.Cycles, cycle) {
			return false, nil
		}
		if len(state.Pending) == 0 {
			if cycle == "" {
				return false, nil
			}
			state.Cycles = appendPMNoticeReceipt(state.Cycles, cycle)
			return true, nil
		}
		slices.SortStableFunc(state.Pending, func(a, b PMNoticeEntry) int {
			if a.Notice.NeedsAction == b.Notice.NeedsAction {
				return 0
			}
			if a.Notice.NeedsAction {
				return -1
			}
			return 1
		})
		if cycle == "" {
			index := slices.IndexFunc(state.Pending, func(e PMNoticeEntry) bool { return e.RetryAt <= now.Unix() })
			if index < 0 {
				return false, nil
			}
			claimed = []PMNoticeEntry{state.Pending[index]}
			state.Pending = slices.Delete(state.Pending, index, index+1)
		} else {
			count := min(pmNoticeBatchLimit, len(state.Pending))
			claimed = slices.Clone(state.Pending[:count])
			state.Pending = slices.Delete(state.Pending, 0, count)
			state.Cycles = appendPMNoticeReceipt(state.Cycles, cycle)
		}
		for _, entry := range claimed {
			state.Seen = appendPMNoticeReceipt(state.Seen, entry.ID)
			state.History = append(state.History, entry)
		}
		state.History = state.History[max(0, len(state.History)-pmNoticeLimit):]
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// RetryCodexPMNotice restores only a proven pre-submission failure.
func RetryCodexPMNotice(ctx context.Context, b Binding, entry PMNoticeEntry, busy bool, now time.Time) error {
	if !busy {
		entry.Attempts++
	}
	if entry.Attempts >= 5 {
		return nil
	}
	entry.RetryAt = now.Add(time.Duration(max(1, entry.Attempts)) * time.Minute).Unix()
	return withPMNotices(ctx, b, func(state *pmNoticeState) (bool, error) {
		if len(state.Pending) >= pmNoticeLimit || slices.ContainsFunc(state.Pending, func(e PMNoticeEntry) bool {
			return e.Conversation == entry.Conversation && e.Notice.Topic == entry.Notice.Topic
		}) {
			return false, nil
		}
		state.Pending = append([]PMNoticeEntry{entry}, state.Pending...)
		state.Seen = slices.DeleteFunc(state.Seen, func(id string) bool { return id == entry.ID })
		state.History = slices.DeleteFunc(state.History, func(e PMNoticeEntry) bool { return e.ID == entry.ID })
		return true, nil
	})
}
