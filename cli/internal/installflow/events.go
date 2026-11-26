// Package installflow records bounded installation observations before an Agent exists.
// The session proof is a local capability, never an event property or command output.
package installflow

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/filelock"
)

const MaxPending = 128
const Retention = 7 * 24 * time.Hour

var ErrBusy = errors.New("installation observation state is busy")
var ErrScope = errors.New("installation observation identity changed")
var ErrExpired = errors.New("installation observation session expired")
var hexID = regexp.MustCompile(`^[a-f0-9]{32,64}$`)

type Scope struct{ Home, Endpoint string }
type Session struct {
	ID        string `json:"install_session_id"`
	Proof     string `json:"proof"`
	Endpoint  string `json:"endpoint"`
	CreatedAt int64  `json:"created_at"`
	AgentID   string `json:"agent_id,omitempty"`
}
type Event struct {
	ID          string `json:"event_id"`
	At          int64  `json:"event_at"`
	Type        string `json:"event_type"`
	Result      string `json:"result"`
	AttemptID   string `json:"attempt_id,omitempty"`
	Component   string `json:"component,omitempty"`
	RuntimeName string `json:"runtime_name,omitempty"`
	RuntimeMode string `json:"runtime_mode,omitempty"`
	Choice      string `json:"choice,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Count       *int   `json:"count,omitempty"`
}
type Batch struct {
	SessionID string  `json:"install_session_id"`
	Events    []Event `json:"events"`
}

var eventTypes = []string{"install_started", "host_detected", "component_install", "component_verify", "permission_scheduling", "permission_execution", "host_restart", "profile_mode", "profile_read_permission", "profile_draft", "identity_local", "scheduler_setup", "credentials_saved", "console_link_delivered", "baseline_feed", "attention_prefill", "onboarding_unlocked", "first_check_requested", "first_check", "first_check_result", "install_receipt_sent", "install_receipt_response"}
var results = []string{"started", "succeeded", "failed", "declined", "skipped", "pending"}
var runtimeNames = []string{"", "unknown", "codex", "openclaw", "workbuddy", "hermes", "deepseek", "deepseek-harness", "dsh", "claude-code", "claude", "cursor", "opencode", "gemini-cli", "gemini", "qoder", "trae", "kilo", "roo", "cline", "windsurf", "antigravity", "kimi", "kiro", "jarvis"}

// Keep this vocabulary synchronized with the server's privacy allowlist.
var reasons = []string{"", "network_error", "timeout", "permission_denied", "user_declined", "user_cancelled", "not_applicable", "no_candidates", "no_content", "validation_failed", "host_unavailable", "authentication_failed", "server_error", "unknown_failure", "restart_required", "already_configured", "manual_selected"}

func NewID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func NormalizeEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid installation observation endpoint")
	}
	return u.String(), nil
}

func Directory(s Scope) (string, error) {
	endpoint, err := NormalizeEndpoint(s.Endpoint)
	if err != nil || !filepath.IsAbs(s.Home) {
		return "", errors.New("absolute Home and valid endpoint required")
	}
	h := sha256.Sum256([]byte(endpoint))
	return filepath.Join(s.Home, "install-observations", hex.EncodeToString(h[:])), nil
}

func withLock(s Scope, create bool, fn func(string) error) error {
	dir, err := checkedDirectory(s, create)
	if err != nil {
		return err
	}
	if _, err = regularFile(filepath.Join(dir, ".state.lock")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lock, acquired, err := filelock.TryAcquire(filepath.Join(dir, ".state.lock"))
	if err != nil {
		return err
	}
	if !acquired {
		return ErrBusy
	}
	defer lock.Release()
	return fn(dir)
}

// Home is chosen by the owner and may itself be a symlink. Derived state paths
// must remain private directories rather than redirecting capability reads.
func checkedDirectory(s Scope, create bool) (string, error) {
	dir, err := Directory(s)
	if err != nil {
		return "", err
	}
	if create {
		if err = os.MkdirAll(s.Home, 0700); err != nil {
			return "", err
		}
	}
	for _, path := range []string{filepath.Dir(dir), dir} {
		if err = privateDirectory(path, create); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func privateDirectory(path string, create bool) error {
	if create {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errors.New("installation observation directory is not private")
	}
	return nil
}

func regularFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("invalid installation observation file")
	}
	return info, nil
}

func readEvent(path string) (Event, error) {
	var event Event
	if _, err := regularFile(path); err != nil {
		return event, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return event, err
	}
	if json.Unmarshal(b, &event) != nil || filepath.Base(path) != event.ID+".json" {
		return event, errors.New("invalid queued observation")
	}
	return event, nil
}

// Bound cleanup to files created by this queue; retain unknown files and locks.
func pruneOldQueues(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	queues := 0
	cutoff := time.Now().Add(-Retention)
	for _, entry := range entries {
		if len(entry.Name()) != 32 || !hexID.MatchString(entry.Name()) {
			continue
		}
		queue := filepath.Join(dir, entry.Name())
		if err = privateDirectory(queue, false); err != nil {
			return err
		}
		queues++
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		files, err := os.ReadDir(queue)
		if err != nil {
			return err
		}
		for i, file := range files {
			if i >= MaxPending {
				break
			}
			path := filepath.Join(queue, file.Name())
			event, err := readEvent(path)
			if err == nil && event.At > 0 && event.At < cutoff.UnixMilli() {
				_ = os.Remove(path)
			}
		}
		if os.Remove(queue) == nil {
			queues--
		}
	}
	if queues >= 16 {
		return errors.New("too many retained installation attempts")
	}
	return nil
}

func readSession(dir, endpoint string) (Session, error) {
	var session Session
	path := filepath.Join(dir, "session.json")
	_, err := regularFile(path)
	if err != nil {
		return session, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return session, err
	}
	if json.Unmarshal(b, &session) != nil || len(session.ID) != 32 || !hexID.MatchString(session.ID) || len(session.Proof) != 64 || !hexID.MatchString(session.Proof) {
		return Session{}, errors.New("invalid observation session")
	}
	endpoint, _ = NormalizeEndpoint(endpoint)
	if session.Endpoint != endpoint {
		return Session{}, ErrScope
	}
	if session.CreatedAt <= 0 || session.CreatedAt > time.Now().Add(time.Minute).UnixMilli() || session.CreatedAt < time.Now().Add(-Retention).UnixMilli() {
		return Session{}, ErrExpired
	}
	return session, nil
}

func atomicJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".observation-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Start reuses the current attempt. A new attempt must be explicitly requested.
func Start(s Scope, fresh bool) (Session, error) {
	var session Session
	err := withLock(s, true, func(dir string) error {
		old, err := readSession(dir, s.Endpoint)
		if err == nil && !fresh {
			session = old
			return nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrExpired) {
			return err
		}
		if errors.Is(err, ErrExpired) && !fresh {
			return err
		}
		if err = pruneOldQueues(dir); err != nil {
			return err
		}
		id, err := NewID()
		if err != nil {
			return err
		}
		proof := make([]byte, 32)
		if _, err = rand.Read(proof); err != nil {
			return err
		}
		endpoint, _ := NormalizeEndpoint(s.Endpoint)
		session = Session{ID: id, Proof: hex.EncodeToString(proof), Endpoint: endpoint, CreatedAt: time.Now().UnixMilli()}
		return atomicJSON(filepath.Join(dir, "session.json"), session)
	})
	return session, err
}

func Current(s Scope) (Session, error) {
	dir, err := checkedDirectory(s, false)
	if err != nil {
		return Session{}, err
	}
	return readSession(dir, s.Endpoint)
}

func Validate(e Event) error {
	if !hexID.MatchString(e.ID) || !slices.Contains(eventTypes, e.Type) || !slices.Contains(results, e.Result) {
		return errors.New("invalid installation event type, result or ID")
	}
	if e.AttemptID != "" && !hexID.MatchString(e.AttemptID) {
		return errors.New("invalid installation attempt ID")
	}
	if !slices.Contains([]string{"", "cli", "skills", "plugin", "scheduler", "identity", "console"}, e.Component) || !slices.Contains([]string{"", "skill", "plugin", "cli", "unknown"}, e.RuntimeMode) || !slices.Contains(reasons, e.Reason) {
		return errors.New("invalid installation event category")
	}
	if e.Choice != "" && (e.Type != "profile_mode" || !slices.Contains([]string{"prefill", "manual"}, e.Choice)) {
		return errors.New("invalid profile mode choice")
	}
	if !slices.Contains(runtimeNames, e.RuntimeName) {
		return errors.New("invalid installation runtime name")
	}
	if e.Count != nil && (*e.Count < 0 || *e.Count > 10000 || !slices.Contains([]string{"baseline_feed", "attention_prefill", "first_check"}, e.Type)) {
		return errors.New("invalid installation event count")
	}
	if e.At <= 0 || e.At > time.Now().Add(time.Minute).UnixMilli() || e.At < time.Now().Add(-Retention).UnixMilli() {
		return errors.New("invalid installation event time")
	}
	return nil
}

func RuntimeName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(strings.SplitN(raw, "/", 2)[0]))
	if name == "" || !slices.Contains(runtimeNames, name) {
		return "unknown"
	}
	return name
}

func Record(s Scope, e Event) (Event, error) {
	return RecordForAgent(s, "", e)
}

// RecordForAgent checks the account under the same lock as the queue write.
// Checking only at upload would leave another account's events in the old queue.
func RecordForAgent(s Scope, agentID string, e Event) (Event, error) {
	if e.ID == "" {
		id, err := NewID()
		if err != nil {
			return e, err
		}
		e.ID = id
	}
	if e.At == 0 {
		e.At = time.Now().UnixMilli()
	}
	if err := Validate(e); err != nil {
		return e, err
	}
	err := withLock(s, false, func(dir string) error {
		session, err := readSession(dir, s.Endpoint)
		if err != nil {
			return err
		}
		if session.AgentID != "" && session.AgentID != agentID {
			return ErrScope
		}
		if agentID != "" && session.AgentID == "" {
			session.AgentID = agentID
			if err = atomicJSON(filepath.Join(dir, "session.json"), session); err != nil {
				return err
			}
		}
		queue := filepath.Join(dir, session.ID)
		if err = privateDirectory(queue, true); err != nil {
			return err
		}
		files, err := os.ReadDir(queue)
		if err != nil {
			return err
		}
		path := filepath.Join(queue, e.ID+".json")
		if old, readErr := readEvent(path); readErr == nil {
			before, _ := json.Marshal(old)
			after, _ := json.Marshal(e)
			if string(before) != string(after) {
				return errors.New("observation ID conflict")
			}
			return nil
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		if len(files) >= MaxPending {
			return errors.New("installation observation queue full")
		}
		return atomicJSON(path, e)
	})
	return e, err
}

func Anonymous(e Event) bool {
	return slices.Index(eventTypes, e.Type) <= slices.Index(eventTypes, "scheduler_setup")
}

// Flush pins the local account before authenticated delivery. Unbound anonymous
// observations can be upgraded only once; account switching never reattributes them.
func Flush(s Scope, agentID string, send func(Batch, map[string]string, bool) error) error {
	var session Session
	var events []Event
	pendingAuth := false
	err := withLock(s, false, func(dir string) error {
		var err error
		session, err = readSession(dir, s.Endpoint)
		if err != nil {
			return err
		}
		if session.AgentID != "" && session.AgentID != agentID {
			return ErrScope
		}
		if agentID != "" && session.AgentID == "" {
			session.AgentID = agentID
			if err = atomicJSON(filepath.Join(dir, "session.json"), session); err != nil {
				return err
			}
		}
		queue := filepath.Join(dir, session.ID)
		if err = privateDirectory(queue, false); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		files, err := os.ReadDir(queue)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			e, err := readEvent(filepath.Join(queue, f.Name()))
			if err != nil {
				return err
			}
			if e.At < time.Now().Add(-Retention).UnixMilli() {
				continue
			}
			if err = Validate(e); err != nil {
				return err
			}
			if agentID != "" || Anonymous(e) {
				events = append(events, e)
			} else {
				pendingAuth = true
			}
			if len(events) == 20 {
				break
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(events) == 0 {
		if pendingAuth {
			return errors.New("installation observations await Agent credentials")
		}
		return nil
	}
	headers := map[string]string{"X-Install-Session-ID": session.ID, "X-Install-Session-Proof": session.Proof}
	if err = send(Batch{SessionID: session.ID, Events: events}, headers, agentID != ""); err != nil {
		return err
	}
	return withLock(s, false, func(dir string) error {
		current, err := readSession(dir, s.Endpoint)
		if err != nil {
			return err
		}
		if current.ID != session.ID || current.AgentID != session.AgentID {
			return ErrScope
		}
		if err = privateDirectory(filepath.Join(dir, session.ID), false); err != nil {
			return err
		}
		for _, e := range events {
			if err = os.Remove(filepath.Join(dir, session.ID, e.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("acknowledge observation: %w", err)
			}
		}
		return nil
	})
}
