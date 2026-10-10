// Package desktopqueue persists account-scoped desktop notifications before remote ACK.
package desktopqueue

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
)

const maxEntries = 8192

type Entry struct {
	Message   desktopnotify.Message `json:"message"`
	Created   int64                 `json:"created"`
	Delivered bool                  `json:"delivered"`
	Attempts  int                   `json:"attempts,omitempty"`
	RetryAt   int64                 `json:"retry_at,omitempty"`
}
type state struct {
	Version int     `json:"version"`
	Scope   string  `json:"scope"`
	Entries []Entry `json:"entries"`
}

// Queue requires its caller to hold the account watch lock; the mutex protects workers.
type Queue struct {
	mu    sync.Mutex
	path  string
	state state
	write func(string, any) error
}

func Open(path, scope string) (*Queue, error) {
	if scope == "" {
		return nil, errors.New("desktop_queue_scope_missing")
	}
	q := &Queue{path: path, state: state{Version: 1, Scope: scope, Entries: []Entry{}}, write: dispatch.WriteJSON}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return q, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 16<<20 || json.Unmarshal(raw, &q.state) != nil || q.state.Version != 1 || q.state.Scope != scope || q.state.Entries == nil || len(q.state.Entries) > maxEntries {
		return nil, errors.New("invalid_desktop_queue")
	}
	seen := map[string]bool{}
	for _, entry := range q.state.Entries {
		if entry.Message.ID == "" || seen[entry.Message.ID] || entry.Attempts < 0 {
			return nil, errors.New("invalid_desktop_queue_entry")
		}
		seen[entry.Message.ID] = true
	}
	return q, nil
}
func (q *Queue) save(entries []Entry) error {
	next := state{Version: 1, Scope: q.state.Scope, Entries: entries}
	if err := q.write(q.path, next); err != nil {
		return err
	}
	q.state = next
	return nil
}
func (q *Queue) Enqueue(message desktopnotify.Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := desktopnotify.Validate(message); err != nil {
		return err
	}
	for _, entry := range q.state.Entries {
		if entry.Message.ID == message.ID {
			return nil
		}
	}
	entries := append([]Entry(nil), q.state.Entries...)
	// Retain recent receipts across restarts and rebindings, never evict pending work.
	if len(entries) >= maxEntries {
		for i, entry := range entries {
			if entry.Delivered {
				entries = append(entries[:i], entries[i+1:]...)
				break
			}
		}
		if len(entries) >= maxEntries {
			return errors.New("desktop_queue_full")
		}
	}
	entries = append(entries, Entry{Message: message, Created: time.Now().Unix()})
	return q.save(entries)
}
func (q *Queue) Next(now time.Time) (Entry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, entry := range q.state.Entries {
		if !entry.Delivered && entry.RetryAt <= now.Unix() {
			return entry, true
		}
	}
	return Entry{}, false
}
func (q *Queue) Complete(id string, delivered bool, now time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := append([]Entry(nil), q.state.Entries...)
	for i := range entries {
		if entries[i].Message.ID != id {
			continue
		}
		if entries[i].Delivered {
			return nil
		}
		entries[i].Delivered = delivered
		if !delivered {
			entries[i].Attempts++
			delay := time.Minute * time.Duration(1<<min(entries[i].Attempts-1, 4))
			entries[i].RetryAt = now.Add(delay).Unix()
		}
		return q.save(entries)
	}
	return errors.New("desktop_message_missing")
}
func (q *Queue) Counts() (pending, delivered int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, entry := range q.state.Entries {
		if entry.Delivered {
			delivered++
		} else {
			pending++
		}
	}
	return
}
