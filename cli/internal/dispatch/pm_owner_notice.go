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
	Pending            []PMNoticeEntry `json:"pending"`
	History            []PMNoticeEntry `json:"history"`
	Seen               []string        `json:"seen"`
	Cycles             []string        `json:"cycles"`
	DesktopUnconfirmed []string        `json:"desktop_unconfirmed,omitempty"`
}

// Watch and heartbeat share this small spool. Hold the retained OS lock only
// across local reads/writes. A claimed item never replays after an uncertain handoff.
func withPMNotices(ctx context.Context, b Binding, change func(*pmNoticeState) (bool, error)) error {
	return editPMNotices(ctx, b, func(state *pmNoticeState, _ func(*pmNoticeState) error) (bool, error) {
		return change(state)
	})
}

func editPMNotices(ctx context.Context, b Binding, change func(*pmNoticeState, func(*pmNoticeState) error) (bool, error)) error {
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
	save := func(next *pmNoticeState) error { return WriteJSON(path, next) }
	write, err := change(&state, save)
	if err != nil || !write {
		return err
	}
	return save(&state)
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

// DeliverPMDigest records handoff after the CLI writes the digest to the host.
func DeliverPMDigest(ctx context.Context, b Binding, cycle string, write func([]PMNoticeEntry) error) error {
	return deliverPMDigest(ctx, b, cycle, write, nil)
}

// DeliverDesktopPMDigest records the submission intent before calling the OS.
// A false result confirms no OS submission and permits the host route.
func DeliverDesktopPMDigest(ctx context.Context, b Binding, cycle string, write func([]PMNoticeEntry) error, show func([]PMNoticeEntry) (bool, error)) error {
	return deliverPMDigest(ctx, b, cycle, write, show)
}

func deliverPMDigest(ctx context.Context, b Binding, cycle string, write func([]PMNoticeEntry) error, show func([]PMNoticeEntry) (bool, error)) error {
	wroteHostDigest := false
	err := editPMNotices(ctx, b, func(state *pmNoticeState, save func(*pmNoticeState) error) (bool, error) {
		if slices.Contains(state.Cycles, cycle) {
			return false, nil
		}
		sortPMNotices(state.Pending)
		batch := slices.Clone(state.Pending[:min(pmNoticeBatchLimit, len(state.Pending))])
		if len(batch) != 0 && show != nil {
			before := *state
			next := *state
			next.Pending = slices.Clone(state.Pending[len(batch):])
			next.Cycles = appendPMNoticeReceipt(slices.Clone(state.Cycles), cycle)
			next.Seen = slices.Clone(state.Seen)
			next.History = slices.Clone(state.History)
			recordPMNotices(&next, batch)
			next.DesktopUnconfirmed = appendPMNoticeReceipt(slices.Clone(state.DesktopUnconfirmed), cycle)
			if err := save(&next); err != nil {
				return false, err
			}
			submitted, err := show(batch)
			if err != nil {
				// The provider may have accepted the notification before failing.
				return false, err
			}
			if submitted {
				next.DesktopUnconfirmed = slices.DeleteFunc(next.DesktopUnconfirmed, func(id string) bool { return id == cycle })
				return false, save(&next)
			}
			if err := save(&before); err != nil {
				return false, err
			}
		}
		if len(batch) != 0 {
			if err := write(batch); err != nil {
				return false, err
			}
			wroteHostDigest = true
		}
		state.Pending = slices.Delete(state.Pending, 0, len(batch))
		state.Cycles = appendPMNoticeReceipt(state.Cycles, cycle)
		recordPMNotices(state, batch)
		return true, nil
	})
	if err != nil || wroteHostDigest {
		return err
	}
	return write(nil)
}

func sortPMNotices(entries []PMNoticeEntry) {
	slices.SortStableFunc(entries, func(a, b PMNoticeEntry) int {
		if a.Notice.NeedsAction == b.Notice.NeedsAction {
			return 0
		}
		if a.Notice.NeedsAction {
			return -1
		}
		return 1
	})
}

func recordPMNotices(state *pmNoticeState, entries []PMNoticeEntry) {
	for _, entry := range entries {
		state.Seen = appendPMNoticeReceipt(state.Seen, entry.ID)
		state.History = append(state.History, entry)
	}
	state.History = state.History[max(0, len(state.History)-pmNoticeLimit):]
}

func NextPMNotice(ctx context.Context, b Binding, now time.Time) (PMNoticeEntry, bool, error) {
	var entry PMNoticeEntry
	found := false
	err := withPMNotices(ctx, b, func(state *pmNoticeState) (bool, error) {
		sortPMNotices(state.Pending)
		for _, pending := range state.Pending {
			if pending.RetryAt <= now.Unix() {
				entry, found = pending, true
				break
			}
		}
		return false, nil
	})
	return entry, found, err
}

var ErrPMNoticeSuperseded = errors.New("pm_owner_notice_superseded")

func CommitPMNotice(ctx context.Context, b Binding, id string) error {
	return HandoffPMNotice(ctx, b, id, nil)
}

// HandoffPMNotice consumes a pending notice only after its destination accepts it.
func HandoffPMNotice(ctx context.Context, b Binding, id string, deliver func() error) error {
	return withPMNotices(ctx, b, func(state *pmNoticeState) (bool, error) {
		index := slices.IndexFunc(state.Pending, func(entry PMNoticeEntry) bool { return entry.ID == id })
		if index < 0 {
			return false, ErrPMNoticeSuperseded
		}
		if deliver != nil {
			if err := deliver(); err != nil {
				return false, err
			}
		}
		recordPMNotices(state, state.Pending[index:index+1])
		state.Pending = slices.Delete(state.Pending, index, index+1)
		return true, nil
	})
}

// RetryCodexPMNotice defers a pending notice after a proven pre-submission failure.
func RetryCodexPMNotice(ctx context.Context, b Binding, entry PMNoticeEntry, busy bool, now time.Time) error {
	if !busy {
		entry.Attempts = min(entry.Attempts+1, 5)
	}
	entry.RetryAt = now.Add(time.Duration(max(1, entry.Attempts)) * time.Minute).Unix()
	return withPMNotices(ctx, b, func(state *pmNoticeState) (bool, error) {
		index := slices.IndexFunc(state.Pending, func(pending PMNoticeEntry) bool { return pending.ID == entry.ID })
		if index < 0 {
			return false, nil
		}
		state.Pending[index].Attempts = entry.Attempts
		state.Pending[index].RetryAt = entry.RetryAt
		return true, nil
	})
}
