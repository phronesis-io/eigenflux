package feedevent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Queue persists pending reports under the directory lock; network sends use a separate lock.
type Queue struct{ dir, path string }
type queueFile struct {
	Events           []Event          `json:"events"`
	FirstSeen        map[string]int64 `json:"first_seen,omitempty"`
	AuthBlockedAt    int64            `json:"auth_blocked_at,omitempty"`
	AuthBlockedScope string           `json:"auth_blocked_scope,omitempty"`
}

var ErrAuthentication = errors.New("feed queue authentication blocked")

type FlushOptions struct {
	Scope                 string
	Foreground            bool
	CredentialsModifiedAt int64
}
type FlushResult struct{ Flushed, Remaining, ScopeMismatch int }

func NewQueue(dir string) *Queue { return &Queue{dir: dir, path: filepath.Join(dir, QueueFileName)} }
func (q *Queue) loadUnlocked() (queueFile, error) {
	var state queueFile
	data, err := os.ReadFile(q.path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("read feedback queue: %w", err)
	}
	return state, nil
}
func (q *Queue) saveAtomic(state queueFile) error {
	if err := os.MkdirAll(q.dir, dirPerm); err != nil {
		return err
	}
	return writeAtomicJSON(q.dir, QueueFileName, state)
}
func (q *Queue) Enqueue(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	l, ok := acquireLock(q.dir)
	if !ok {
		return fmt.Errorf("feedevent: queue busy")
	}
	defer l.release()
	state, err := q.loadUnlocked()
	if err != nil {
		return err
	}
	state.Events = append(state.Events, events...)
	state.observeMissing(time.Now().UnixMilli())
	return q.saveAtomic(state)
}

// Identity is stable across retries and old-binary JSON rewrites.
func Identity(e Event) string {
	if key, _ := e["client_key"].(string); e["kind"] == "feedback" && key != "" {
		return "feedback:" + key
	}
	if key, _ := e["dedup_key"].(string); key != "" {
		return "event:" + key
	}
	data, _ := json.Marshal(e)
	return fmt.Sprintf("raw:%x", sha256.Sum256(data))
}
func scopedIdentity(e Event) string {
	scope, _ := e["_scope"].(string)
	return scope + "\x00" + Identity(e)
}
func (s *queueFile) observeMissing(now int64) {
	if s.FirstSeen == nil {
		s.FirstSeen = map[string]int64{}
	}
	for _, e := range s.Events {
		if asInt64(e["ts"]) <= 0 {
			key := scopedIdentity(e)
			if s.FirstSeen[key] <= 0 {
				s.FirstSeen[key] = now
			}
		}
	}
}
func (s *queueFile) clean(now int64) int {
	s.observeMissing(now)
	fresh := make([]Event, 0, len(s.Events))
	dropped := 0
	for _, e := range s.Events {
		ts := asInt64(e["ts"])
		if ts <= 0 {
			ts = s.FirstSeen[scopedIdentity(e)]
		}
		if now-ts > MaxAgeMs {
			dropped++
			continue
		}
		fresh = append(fresh, e)
	}
	s.Events = collapseByDedup(fresh)
	keys := map[string]bool{}
	for _, e := range s.Events {
		keys[scopedIdentity(e)] = true
	}
	for key := range s.FirstSeen {
		if !keys[key] {
			delete(s.FirstSeen, key)
		}
	}
	return dropped
}
func (q *Queue) Flush(now int64, push func([]Event) error) (int, int, error) {
	return q.FlushWithDrops(now, push, nil)
}
func (q *Queue) FlushWithDrops(now int64, push func([]Event) error, dropped func(int) error) (int, int, error) {
	result, err := q.FlushAcknowledged(now, FlushOptions{}, func(batch []Event) ([]string, error) {
		if err := push(batch); err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(batch))
		for _, e := range batch {
			ids = append(ids, Identity(e))
		}
		return ids, nil
	}, dropped)
	return result.Flushed, result.Remaining, err
}

// FlushAcknowledged removes only confirmed entries, including when a later group fails.
func (q *Queue) FlushAcknowledged(now int64, options FlushOptions, push func([]Event) ([]string, error), dropped func(int) error) (result FlushResult, err error) {
	flush, ok := acquireNamedLockFor(q.dir, "flush.lock", 0)
	if !ok {
		result.Remaining, result.ScopeMismatch, err = q.Pending(now, options.Scope)
		return
	}
	defer flush.release()
	l, ok := acquireLock(q.dir)
	if !ok {
		return result, fmt.Errorf("feedevent: queue busy")
	}
	state, err := q.loadUnlocked()
	if err != nil {
		l.release()
		return result, err
	}
	drops := state.clean(now)
	sendable, held := state.partition(options.Scope)
	result.Remaining, result.ScopeMismatch = len(sendable), held
	blocked := !options.Foreground && state.AuthBlockedAt > 0 && state.AuthBlockedScope == options.Scope && options.CredentialsModifiedAt <= state.AuthBlockedAt
	if state.AuthBlockedScope == options.Scope && (options.Foreground || options.CredentialsModifiedAt > state.AuthBlockedAt) {
		state.AuthBlockedAt = 0
		state.AuthBlockedScope = ""
	}
	if err = q.saveAtomic(state); err != nil {
		l.release()
		return result, err
	}
	l.release()
	if drops > 0 && dropped != nil {
		if err = dropped(drops); err != nil {
			return result, err
		}
	}
	if blocked {
		return result, nil
	}
	batch := []Event{}
	for _, feedback := range []bool{true, false} {
		for _, e := range sendable {
			if len(batch) == MaxBatch {
				break
			}
			if (e["kind"] == "feedback") == feedback {
				batch = append(batch, e)
			}
		}
	}
	if len(batch) == 0 {
		return result, nil
	}
	acknowledged, pushErr := push(batch)
	ids := map[string]bool{}
	for _, id := range acknowledged {
		ids[id] = true
	}
	confirmed := map[string]bool{}
	for _, e := range batch {
		if ids[Identity(e)] {
			confirmed[scopedIdentity(e)] = true
		}
	}
	l, ok = acquireLock(q.dir)
	if !ok {
		return result, errors.Join(errors.New("feedevent: queue busy after send"), pushErr)
	}
	defer l.release()
	state, err = q.loadUnlocked()
	if err != nil {
		return result, err
	}
	rest := make([]Event, 0, len(state.Events))
	for _, e := range state.Events {
		key := scopedIdentity(e)
		if !confirmed[key] {
			rest = append(rest, e)
		} else {
			delete(state.FirstSeen, key)
		}
	}
	state.Events = rest
	if errors.Is(pushErr, ErrAuthentication) {
		state.AuthBlockedAt = now
		state.AuthBlockedScope = options.Scope
	}
	if err = q.saveAtomic(state); err != nil {
		return result, err
	}
	result.Flushed = len(confirmed)
	sendable, held = state.partition(options.Scope)
	result.Remaining, result.ScopeMismatch = len(sendable), held
	return result, pushErr
}

// partition splits entries this scope may send from entries held for another
// account. Unscoped legacy entries are held while any foreign entry exists.
func (s queueFile) partition(scope string) (sendable []Event, held int) {
	events := collapseByDedup(s.Events)
	foreign := false
	for _, e := range events {
		if v, _ := e["_scope"].(string); scope != "" && v != "" && v != scope {
			foreign = true
			break
		}
	}
	for _, e := range events {
		v, _ := e["_scope"].(string)
		if scope != "" && ((v != "" && v != scope) || (v == "" && foreign)) {
			held++
			continue
		}
		sendable = append(sendable, e)
	}
	return sendable, held
}

// Pending counts what this scope can still send; held entries wait for their account or expiry.
func (q *Queue) Pending(now int64, scope string) (remaining, held int, err error) {
	state, err := q.loadUnlocked()
	if err != nil {
		return 0, 0, err
	}
	state.clean(now)
	sendable, held := state.partition(scope)
	return len(sendable), held, nil
}
func collapseByDedup(events []Event) []Event {
	seen := map[string]bool{}
	out := make([]Event, 0, len(events))
	for _, e := range events {
		key := scopedIdentity(e)
		if !seen[key] {
			seen[key] = true
			out = append(out, e)
		}
	}
	return out
}
func asInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		return 0
	}
}

// Stats never turns unreadable durable judgments into an empty queue.
func (q *Queue) Stats(now int64) (depth int, oldestAge int64, err error) {
	state, err := q.loadUnlocked()
	if err != nil {
		return 0, 0, err
	}
	state.clean(now)
	for _, e := range state.Events {
		depth++
		ts := asInt64(e["ts"])
		if ts <= 0 {
			ts = state.FirstSeen[scopedIdentity(e)]
		}
		oldestAge = max(oldestAge, now-ts)
	}
	return
}

// ClearAuthBlock follows a successful foreground authenticated request.
func (q *Queue) ClearAuthBlock(scope string) error {
	state, err := q.loadUnlocked()
	if err != nil || state.AuthBlockedAt == 0 || state.AuthBlockedScope != scope {
		return err
	}
	l, ok := acquireLock(q.dir)
	if !ok {
		return fmt.Errorf("feedevent: queue busy")
	}
	defer l.release()
	state, err = q.loadUnlocked()
	if err != nil {
		return err
	}
	if state.AuthBlockedScope != scope {
		return nil
	}
	state.AuthBlockedAt, state.AuthBlockedScope = 0, ""
	return q.saveAtomic(state)
}
