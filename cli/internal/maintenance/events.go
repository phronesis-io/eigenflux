// Package maintenance persists bounded, account-scoped maintenance observations.
package maintenance

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

const MaxEvents = 256
const Retention = 7 * 24 * time.Hour

var ErrBusy = errors.New("maintenance state is busy")
var safeValue = regexp.MustCompile(`^[A-Za-z0-9_.+-]{0,128}$`)
var idValue = regexp.MustCompile(`^[a-f0-9]{32,64}$`)

type Scope struct{ Home, Server, AgentID string }
type Event struct {
	EventID        string `json:"event_id"`
	EventAt        int64  `json:"event_at"`
	AttemptID      string `json:"attempt_id"`
	Component      string `json:"component"`
	Trigger        string `json:"trigger"`
	Phase          string `json:"phase"`
	Result         string `json:"result"`
	Host           string `json:"host,omitempty"`
	Mode           string `json:"mode,omitempty"`
	FromVersion    string `json:"from_version,omitempty"`
	ToVersion      string `json:"to_version,omitempty"`
	RunningVersion string `json:"running_version,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
}

type State struct {
	ReadAttempts map[string]Event `json:"read_attempts,omitempty"`
	Events       []Event          `json:"events"`
	Dropped      uint64           `json:"dropped"`
	LastAttempt  int64            `json:"last_attempt"`
	NextFlush    int64            `json:"next_flush"`
	Failures     int              `json:"failures"`
	Attempts     map[string]Event `json:"attempts,omitempty"`
}

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func NewEvent(attempt, component, trigger, phase, result string) Event {
	sum := sha256.Sum256([]byte(attempt + "\x00" + component + "\x00" + phase + "\x00" + result))
	return Event{EventID: hex.EncodeToString(sum[:]), EventAt: time.Now().UnixMilli(), AttemptID: attempt, Component: component, Trigger: trigger, Phase: phase, Result: result}
}
func Validate(e Event) error {
	if !idValue.MatchString(e.EventID) || !idValue.MatchString(e.AttemptID) {
		return errors.New("invalid maintenance identity")
	}
	if !contains([]string{"cli", "skills", "plugin", "scheduler"}, e.Component) || !contains([]string{"auto", "manual", "adoption"}, e.Trigger) {
		return errors.New("invalid maintenance component or trigger")
	}
	if !contains([]string{"check", "download", "verify", "probe", "install", "execute", "adoption", "load", "read", "migration"}, e.Phase) || !contains([]string{"started", "no_update", "installed", "executed", "runtime_ready", "loaded", "rules_read", "verified", "failed", "rolled_back", "restart_required", "blocked", "waiting_online", "not_installed"}, e.Result) {
		return errors.New("invalid maintenance phase or result")
	}
	if (e.Result == "runtime_ready" || e.Result == "executed") && e.Component != "cli" || e.Result == "rules_read" && e.Component != "skills" || e.Result == "loaded" && e.Component != "plugin" || e.Result == "verified" && e.Component != "scheduler" {
		return fmt.Errorf("result does not belong to component")
	}
	if e.Mode != "" && e.Mode != "skill" && e.Mode != "plugin" {
		return errors.New("invalid maintenance mode")
	}
	for _, s := range []string{e.Host, e.FromVersion, e.ToVersion, e.RunningVersion, e.ErrorCode} {
		if !safeValue.MatchString(s) {
			return errors.New("invalid maintenance metadata")
		}
	}
	if (e.Result == "runtime_ready" || e.Result == "executed" || e.Result == "loaded") && (e.ToVersion == "" || e.RunningVersion != e.ToVersion) {
		return fmt.Errorf("runtime observation requires matching target and running version")
	}
	if e.Result == "rules_read" && e.ToVersion == "" {
		return fmt.Errorf("rules read requires the observed revision")
	}
	if e.DurationMS < 0 || e.DurationMS > int64(24*time.Hour/time.Millisecond) {
		return errors.New("invalid maintenance duration")
	}
	return nil
}
func contains(values []string, v string) bool {
	for _, s := range values {
		if s == v {
			return true
		}
	}
	return false
}
func (s Scope) path() (string, error) {
	if !filepath.IsAbs(s.Home) || s.Server == "" || s.AgentID == "" {
		return "", errors.New("maintenance requires absolute Home, server and Agent identity")
	}
	sum := sha256.Sum256([]byte(s.Server + "\x00" + s.AgentID))
	return filepath.Join(s.Home, "maintenance", hex.EncodeToString(sum[:16])+"-events.json"), nil
}
func prune(st *State, now time.Time) {
	var receipts []Event
	for id, e := range st.ReadAttempts {
		if e.EventAt < now.Add(-Retention).UnixMilli() {
			delete(st.ReadAttempts, id)
		} else {
			receipts = append(receipts, e)
		}
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].EventAt < receipts[j].EventAt })
	if len(receipts) > 32 {
		for _, e := range receipts[:len(receipts)-32] {
			delete(st.ReadAttempts, e.AttemptID)
		}
	}
	kept := st.Events[:0]
	for _, e := range st.Events {
		if e.EventAt < now.Add(-Retention).UnixMilli() {
			st.Dropped++
		} else {
			kept = append(kept, e)
		}
	}
	st.Events = kept
	if len(st.Events) > MaxEvents {
		st.Dropped += uint64(len(st.Events) - MaxEvents)
		st.Events = st.Events[len(st.Events)-MaxEvents:]
	}
}
func update(s Scope, fn func(*State) error) error {
	path, err := s.path()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		ok, e := tryLockFile(lock)
		if e != nil {
			return e
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			return ErrBusy
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer unlockFile(lock)
	var st State
	b, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(b, &st); err != nil {
			return fmt.Errorf("invalid maintenance state: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if st.ReadAttempts == nil {
		st.ReadAttempts = map[string]Event{}
	}
	if st.Attempts == nil {
		st.Attempts = map[string]Event{}
	}
	prune(&st, time.Now())
	if err = fn(&st); err != nil {
		return err
	}
	prune(&st, time.Now())
	b, err = json.Marshal(st)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".maintenance-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = replaceFile(f.Name(), path); err != nil {
		return err
	}
	return syncParentDir(filepath.Dir(path))
}
func Record(s Scope, e Event) error {
	if err := Validate(e); err != nil {
		return err
	}
	return update(s, func(st *State) error {
		for _, old := range st.Events {
			if old.EventID == e.EventID {
				return nil
			}
		}
		st.Events = append(st.Events, e)
		st.Attempts[e.Component] = e
		if e.Component == "skills" && e.Phase == "install" && (e.Result == "installed" || e.Result == "no_update") {
			st.ReadAttempts[e.AttemptID] = e
		}
		return nil
	})
}
func LastAttempt(s Scope, component string) (Event, error) {
	var e Event
	err := update(s, func(st *State) error { e = st.Attempts[component]; return nil })
	return e, err
}
func Snapshot(s Scope) (State, error) {
	var result State
	err := update(s, func(st *State) error { result = *st; return nil })
	return result, err
}
func Due(s Scope, now time.Time, interval time.Duration) (bool, error) {
	var due bool
	err := update(s, func(st *State) error {
		due = st.LastAttempt == 0 || now.UnixMilli() < st.LastAttempt || now.Sub(time.UnixMilli(st.LastAttempt)) >= interval
		return nil
	})
	return due, err
}
func MarkAttempt(s Scope, now time.Time) error {
	return update(s, func(st *State) error { st.LastAttempt = now.UnixMilli(); return nil })
}

// Flush sends at most one batch. Network work is outside the state lock; stable
// event IDs make overlapping flushes harmless. Only acknowledged IDs are removed.
func Flush(s Scope, now time.Time, send func([]Event) error) error {
	var batch []Event
	if err := update(s, func(st *State) error {
		if now.UnixMilli() < st.NextFlush {
			return nil
		}
		n := len(st.Events)
		if n > 50 {
			n = 50
		}
		batch = append(batch, st.Events[:n]...)
		return nil
	}); err != nil {
		return err
	}
	if len(batch) == 0 {
		return nil
	}
	sendErr := send(batch)
	err := update(s, func(st *State) error {
		if sendErr != nil {
			st.Failures++
			if st.Failures > 8 {
				st.Failures = 8
			}
			st.NextFlush = now.Add(time.Duration(1<<st.Failures) * time.Second).UnixMilli()
			return nil
		}
		ids := map[string]bool{}
		for _, e := range batch {
			ids[e.EventID] = true
		}
		kept := st.Events[:0]
		for _, e := range st.Events {
			if !ids[e.EventID] {
				kept = append(kept, e)
			}
		}
		st.Events = kept
		st.NextFlush = 0
		st.Failures = 0
		return nil
	})
	if err != nil {
		return err
	}
	return sendErr
}

// ReadAttempt retains bounded plan receipts across overlapping same-revision
// heartbeats. Successful uploads must not erase an Agent's in-flight plan.
func ReadAttempt(s Scope, id string) (Event, bool, error) {
	var e Event
	var ok bool
	err := update(s, func(st *State) error { e, ok = st.ReadAttempts[id]; return nil })
	return e, ok, err
}
