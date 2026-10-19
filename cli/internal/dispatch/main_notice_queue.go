package dispatch

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// MainNotice stores routing metadata only. Its lifecycle never changes an Order.
type MainNotice struct {
	ID      string `json:"notification_id"`
	OrderID string `json:"order_id"`
	Version int64  `json:"order_version"`
	Role    string `json:"role"`
	Event   string `json:"event"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
}
type mainNoticeQueueState struct {
	Scope    string       `json:"scope"`
	Revision string       `json:"binding_revision"`
	Entries  []MainNotice `json:"entries"`
}
type MainNoticeQueue struct {
	mu    sync.Mutex
	path  string
	state mainNoticeQueueState
}

func OpenMainNoticeQueue(binding Binding) (*MainNoticeQueue, error) {
	if err := binding.validateIdentity(); err != nil {
		return nil, err
	}
	q := &MainNoticeQueue{path: filepath.Join(binding.Home, "watch", binding.Scope+"."+binding.Revision+".main-notices.json"), state: mainNoticeQueueState{Scope: binding.Scope, Revision: binding.Revision, Entries: []MainNotice{}}}
	f, err := os.Open(q.path)
	if os.IsNotExist(err) {
		return q, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 2<<20 || json.Unmarshal(raw, &q.state) != nil || q.state.Scope != binding.Scope || q.state.Revision != binding.Revision || len(q.state.Entries) > 1024 {
		return nil, errors.New("invalid_main_notice_queue")
	}
	seen := map[string]bool{}
	changed := false
	for i := range q.state.Entries {
		item := &q.state.Entries[i]
		if !validMainNotice(*item) || seen[item.ID] {
			return nil, errors.New("invalid_main_notice_queue")
		}
		seen[item.ID] = true
		if item.Status == "running" {
			item.Status, item.Code = "skipped", "execution_unconfirmed"
			changed = true
		}
	}
	if changed {
		if err = q.save(q.state.Entries); err != nil {
			return nil, err
		}
	}
	return q, nil
}
func validMainNotice(item MainNotice) bool {
	for _, value := range []string{item.ID, item.OrderID} {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != value {
			return false
		}
	}
	if item.Version <= 0 || (item.Role != "buyer" && item.Role != "seller") || len(item.Event) > 128 || len(item.State) > 64 {
		return false
	}
	for _, value := range []string{item.Event, item.State} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	switch item.Status {
	case "pending", "running", "delivered", "skipped":
		return true
	}
	return false
}
func (q *MainNoticeQueue) save(entries []MainNotice) error {
	state := q.state
	state.Entries = entries
	if err := WriteJSON(q.path, state); err != nil {
		return err
	}
	q.state = state
	return nil
}
func (q *MainNoticeQueue) Add(item MainNotice) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	item.Status = "pending"
	item.Code = ""
	if !validMainNotice(item) {
		return errors.New("invalid_main_notice")
	}
	for _, old := range q.state.Entries {
		if old.ID == item.ID {
			return nil
		}
	}
	entries := append([]MainNotice{}, q.state.Entries...)
	if len(entries) >= 1024 {
		removed := false
		for i, old := range entries {
			if old.Status == "delivered" || old.Status == "skipped" {
				entries = append(entries[:i], entries[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			return errors.New("main_notice_queue_full")
		}
	}
	return q.save(append(entries, item))
}
func (q *MainNoticeQueue) Next() (MainNotice, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, item := range q.state.Entries {
		if item.Status == "pending" {
			entries := append([]MainNotice{}, q.state.Entries...)
			item.Status = "running"
			entries[i] = item
			if err := q.save(entries); err != nil {
				return MainNotice{}, false, err
			}
			return item, true, nil
		}
	}
	return MainNotice{}, false, nil
}
func (q *MainNoticeQueue) Finish(id, status, code string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if status != "delivered" && status != "skipped" {
		return errors.New("invalid_main_notice_status")
	}
	entries := append([]MainNotice{}, q.state.Entries...)
	for i, item := range entries {
		if item.ID == id && item.Status == "running" {
			entries[i].Status, entries[i].Code = status, code
			return q.save(entries)
		}
	}
	return errors.New("main_notice_not_running")
}
