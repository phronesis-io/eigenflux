package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
)

// A pulse proves this account's current owner is making progress. It is not a
// successful model invocation or a transaction receipt.
type watchLiveStatus struct {
	State             string    `json:"state"`
	Running           bool      `json:"running"`
	Ready             bool      `json:"ready"`
	AgentCallVerified bool      `json:"agent_call_verified"`
	DesktopStatus     string    `json:"desktop_status,omitempty"`
	PID               int       `json:"pid,omitempty"`
	Commit            string    `json:"cli_commit,omitempty"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
	Home              string    `json:"home,omitempty"`
	Server            string    `json:"server,omitempty"`
	Endpoint          string    `json:"endpoint,omitempty"`
	Scope             string    `json:"state_scope,omitempty"`
	Revision          string    `json:"binding_revision,omitempty"`
	NotificationsOnly bool      `json:"notifications_only"`
}

type watchLiveRecorder struct {
	mu     sync.Mutex
	path   string
	status watchLiveStatus
}

func watchLivePath(b dispatch.Binding) string {
	return filepath.Join(b.Home, "watch", watchstate.Scope(b.Home, b.Server, "", "")+".runtime.json")
}

func newWatchLiveRecorder(w *accountWatch) *watchLiveRecorder {
	b := dispatch.Binding{Home: w.home, Server: w.server.Name}
	s := watchLiveStatus{State: "starting", Running: true, PID: os.Getpid(), Commit: commit, Home: w.home, Server: w.server.Name, Endpoint: w.server.Endpoint, Scope: w.scope, NotificationsOnly: w.notificationsOnly}
	if w.binding != nil {
		s.Revision = w.binding.Revision
	}
	return &watchLiveRecorder{path: watchLivePath(b), status: s}
}

func (r *watchLiveRecorder) saveLocked() error {
	r.status.UpdatedAt = time.Now().UTC()
	return dispatch.WriteJSON(r.path, r.status)
}
func (r *watchLiveRecorder) event(kind string, data interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, _ := json.Marshal(data)
	var d map[string]interface{}
	_ = json.Unmarshal(raw, &d)
	str := func(key string) string { v, _ := d[key].(string); return v }
	switch kind {
	case "runtime_ready", "notifications_ready":
		r.status.Ready = true
		r.status.State = "ready"
	case "runtime_unavailable", "notifications_unavailable":
		r.status.Ready = false
		r.status.State = "running"
	case "desktop_notification_status":
		if str("notification_id") == "" {
			r.status.DesktopStatus = str("status")
		}
	case "dispatch_decision":
		if str("job_id") != "" && str("session_id") != "" && (str("decision") == "reply" || str("decision") == "no_reply" || str("decision") == "needs_user") {
			r.status.AgentCallVerified = true
		}
	case "dispatch_status":
		code, outcome := str("code"), str("outcome")
		if (str("status") == "completed" && str("session_id") != "" && code != "not_due") || (str("job_id") != "" && outcome != "" && (code == "commission_intake_"+outcome || code == "commission_fulfillment_"+outcome)) {
			r.status.AgentCallVerified = true
		}
	}
}
func (r *watchLiveRecorder) pulse(ctx context.Context) error {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		r.mu.Lock()
		err := r.saveLocked()
		r.mu.Unlock()
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
func (r *watchLiveRecorder) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.State = "stopped"
	r.status.Running = false
	r.status.Ready = false
	_ = r.saveLocked()
}

func readWatchLiveStatus(expected dispatch.Binding, notificationsOnly bool, now time.Time) watchLiveStatus {
	stopped := watchLiveStatus{State: "stopped", NotificationsOnly: notificationsOnly}
	raw, err := os.ReadFile(watchLivePath(expected))
	if err != nil {
		return stopped
	}
	var s watchLiveStatus
	if json.Unmarshal(raw, &s) != nil {
		return stopped
	}
	if s.Home != expected.Home || s.Server != expected.Server || s.Endpoint != expected.Endpoint || s.Scope != expected.Scope || s.Revision != expected.Revision || s.NotificationsOnly != notificationsOnly {
		return watchLiveStatus{State: "configuration_changed", NotificationsOnly: notificationsOnly}
	}
	age := now.Sub(s.UpdatedAt)
	if !s.Running || age < 0 || age > 15*time.Second {
		s.State = "stopped"
		s.Running = false
		s.Ready = false
		s.AgentCallVerified = false
		return s
	}
	release, lockErr := watchstate.Acquire(expected.Home, expected.Server)
	if lockErr == nil {
		release()
		s.State = "stopped"
		s.Running = false
		s.Ready = false
		s.AgentCallVerified = false
		return s
	}
	// Permission/I/O errors must not be mistaken for another owner. Windows
	// ERROR_LOCK_VIOLATION is errno 33; Unix flock uses EWOULDBLOCK.
	if !errors.Is(lockErr, syscall.EWOULDBLOCK) && !errors.Is(lockErr, syscall.Errno(33)) {
		s.State = "unavailable"
		s.Running = false
		s.Ready = false
		s.AgentCallVerified = false
	}
	return s
}
