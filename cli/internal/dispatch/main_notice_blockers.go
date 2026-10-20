package dispatch

import (
	"errors"
	"strconv"
)

const MainNoticeLocalBlockerEvent = "order.execution.blocked.local.v1"

func (n MainNotice) IsLocalBlocker() bool { return n.Event == MainNoticeLocalBlockerEvent }

func localBlockerMainNotice(scope string, b CommissionBlocker) MainNotice {
	return MainNotice{
		ID: "local:" + journalID(scope, b.ID, strconv.FormatInt(b.OrderVersion, 10), b.Status, b.Code), OrderID: b.OrderID,
		Version: b.OrderVersion, Role: b.Role, Event: MainNoticeLocalBlockerEvent,
		Status: "pending", JobID: b.ID, JobStatus: b.Status,
		JobCode: CommissionExecutionCode(b.Code, b.Status),
	}
}

func (n MainNotice) MatchesLocalBlocker(scope string, b CommissionBlocker) bool {
	current := localBlockerMainNotice(scope, b)
	return n.IsLocalBlocker() && n.ID == current.ID && n.OrderID == current.OrderID && n.Version == current.Version && n.Role == current.Role && n.JobID == current.JobID && n.JobStatus == current.JobStatus && n.JobCode == current.JobCode
}

// Retain one receipt for each current seller blocker, including uncertain calls.
// Retire it only when that exact job/status/code no longer exists in the journal.
func (q *MainNoticeQueue) SyncLocalBlockers(blockers []CommissionBlocker) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	active := make(map[string]MainNotice)
	ordered := make([]MainNotice, 0, len(blockers))
	for _, blocker := range blockers {
		if blocker.Role != "seller" {
			continue
		}
		item := localBlockerMainNotice(q.state.Scope, blocker)
		if !validMainNotice(item) {
			return errors.New("invalid_main_notice_blocker")
		}
		if _, exists := active[item.ID]; !exists {
			active[item.ID] = item
			ordered = append(ordered, item)
		}
	}
	entries := make([]MainNotice, 0, len(q.state.Entries))
	seen := make(map[string]bool)
	changed, full := false, false
	for _, entry := range q.state.Entries {
		if entry.IsLocalBlocker() {
			if _, exists := active[entry.ID]; !exists {
				changed = true
				continue
			}
		}
		entries = append(entries, entry)
		seen[entry.ID] = true
	}
	for _, item := range ordered {
		if seen[item.ID] {
			continue
		}
		next, err := appendMainNotice(entries, item)
		if err != nil {
			full = true
			continue
		}
		entries, changed = next, true
	}
	if changed {
		if err := q.save(entries); err != nil {
			return err
		}
	}
	if full {
		return ErrMainNoticeQueueFull
	}
	return nil
}
