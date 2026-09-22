package dispatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	journalMaxBytes   = 16 << 20
	journalMaxPending = 256
	// Deduplication covers all unresolved jobs and the most recent 1024 completed jobs.
	journalMaxCompleted = 1024
)

type journalState struct {
	Version  int               `json:"version"`
	Scope    string            `json:"scope"`
	Revision string            `json:"binding_revision"`
	AgentID  string            `json:"agent_id"`
	Sequence uint64            `json:"sequence"`
	Jobs     []Job             `json:"jobs"`
	Sessions map[string]string `json:"sessions"`
}

// Journal requires the caller to hold the account watch lock for its lifetime.
// Its mutex serializes ingestion and execution within that owner process.
type Journal struct {
	mu      sync.Mutex
	binding Binding
	path    string
	state   journalState
	wake    chan struct{}
}

func journalPath(b Binding) string {
	return filepath.Join(b.Home, "watch", "dispatch-"+journalID(b.Scope, b.Revision)+".json")
}

func journalID(parts ...string) string {
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validJournalBinding(b Binding) error {
	if b.Home == "" || strings.TrimSpace(b.Scope) == "" || strings.TrimSpace(b.Revision) == "" || strings.TrimSpace(b.AgentID) == "" {
		return errors.New("invalid_journal_binding")
	}
	return nil
}

func readJournal(b Binding) (journalState, bool, error) {
	if err := validJournalBinding(b); err != nil {
		return journalState{}, false, err
	}
	f, err := os.Open(journalPath(b))
	if errors.Is(err, os.ErrNotExist) {
		return journalState{Version: 1, Scope: b.Scope, Revision: b.Revision, AgentID: b.AgentID, Jobs: []Job{}, Sessions: map[string]string{}}, false, nil
	}
	if err != nil {
		return journalState{}, false, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, journalMaxBytes+1))
	if err != nil {
		return journalState{}, false, err
	}
	if len(raw) > journalMaxBytes {
		return journalState{}, false, errors.New("journal_size_limit")
	}
	var s *journalState
	if err := json.Unmarshal(raw, &s); err != nil || s == nil {
		return journalState{}, false, errors.New("invalid_journal")
	}
	if s.Version != 1 || s.Scope != b.Scope || s.Revision != b.Revision || s.AgentID != b.AgentID || s.Sessions == nil || s.Jobs == nil {
		return journalState{}, false, errors.New("journal_identity_or_version_mismatch")
	}
	seen := map[string]bool{}
	for _, job := range s.Jobs {
		if job.ID == "" || seen[job.ID] || job.Scope != b.Scope || job.Revision != b.Revision || !validJobStatus(job.Status) {
			return journalState{}, false, errors.New("invalid_journal_job")
		}
		seen[job.ID] = true
		switch job.Kind {
		case "pm_push":
			m := job.Message
			if m == nil || strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.Conversation) == "" || m.Sender == "" || m.Sender == b.AgentID || m.Receiver != b.AgentID || job.ID != journalID(b.Scope, b.Revision, m.Conversation, m.ID) {
				return journalState{}, false, errors.New("invalid_journal_message")
			}
		case "control_pending", "profile_review_due", "maintenance_due":
			if job.Message != nil || (!json.Valid(job.Data) && !(completedStatus(job.Status) && len(job.Data) == 0)) {
				return journalState{}, false, errors.New("invalid_journal_hint")
			}
		default:
			return journalState{}, false, errors.New("invalid_journal_kind")
		}
	}
	if unresolvedCount(*s) > journalMaxPending || len(s.Jobs)-unresolvedCount(*s) > journalMaxCompleted {
		return journalState{}, false, errors.New("journal_capacity_exceeded")
	}
	return *s, true, nil
}

func OpenJournal(b Binding) (*Journal, error) {
	s, exists, err := readJournal(b)
	if err != nil {
		return nil, err
	}
	j := &Journal{binding: b, path: journalPath(b), state: s, wake: make(chan struct{}, 1)}
	changed := !exists
	for i := range s.Jobs {
		if s.Jobs[i].Status == "running" || s.Jobs[i].Status == "sending" {
			s.Jobs[i].Status = "unknown"
			s.Jobs[i].Code = "interrupted_execution"
			changed = true
		}
	}
	if changed {
		if err := j.save(s); err != nil {
			return nil, err
		}
	}
	if j.hasPending() {
		j.signal()
	}
	return j, nil
}

// ReadJournalStatus never recovers or writes state and is safe for a status reader
// while the watch owner holds the lock. Atomic replacement yields a full snapshot.
func ReadJournalStatus(b Binding) ([]Job, error) {
	s, _, err := readJournal(b)
	if err != nil {
		return nil, err
	}
	return redactedJobs(s.Jobs), nil
}

func (j *Journal) save(next journalState) error {
	// Clone before compacting: completed messages may still share pointers with
	// current state, which must remain unchanged if persistence fails.
	next = cloneJournal(next)
	for i := range next.Jobs {
		if completedStatus(next.Jobs[i].Status) {
			next.Jobs[i] = cloneJob(next.Jobs[i])
			next.Jobs[i].Data = nil
			if next.Jobs[i].Message != nil {
				next.Jobs[i].Message.Content = ""
			}
		}
	}
	trimCompleted(&next)
	if unresolvedCount(next) > journalMaxPending {
		return errors.New("journal_capacity_exceeded")
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if len(raw)+1 > journalMaxBytes {
		return errors.New("journal_size_limit")
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0700); err != nil {
		return err
	}
	if err := WriteJSON(j.path, next); err != nil {
		return err
	}
	j.state = next
	return nil
}

func cloneJournal(s journalState) journalState {
	next := s
	next.Jobs = append([]Job{}, s.Jobs...)
	next.Sessions = make(map[string]string, len(s.Sessions))
	for k, v := range s.Sessions {
		next.Sessions[k] = v
	}
	return next
}

func (j *Journal) AddMessages(raw json.RawMessage) error {
	if len(raw) > journalMaxBytes {
		return errors.New("message_batch_size_limit")
	}
	var envelope *struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope == nil {
		return errors.New("invalid_message_batch")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	seen := make(map[string]bool, len(next.Jobs))
	for _, job := range next.Jobs {
		seen[job.ID] = true
	}
	added := false
	for _, msg := range envelope.Messages {
		// History is deliberately not decoded. Outbound and other-account rows cannot
		// become executable jobs even if supplied in the messages field.
		if msg.Receiver != j.binding.AgentID || msg.Sender == j.binding.AgentID {
			continue
		}
		if strings.TrimSpace(msg.ID) == "" || strings.TrimSpace(msg.Conversation) == "" || strings.TrimSpace(msg.Sender) == "" {
			return errors.New("invalid_inbound_message_identity")
		}
		id := journalID(j.binding.Scope, j.binding.Revision, msg.Conversation, msg.ID)
		if seen[id] {
			continue
		}
		seen[id] = true
		m := msg
		next.Jobs = append(next.Jobs, Job{ID: id, Kind: "pm_push", Scope: j.binding.Scope, Revision: j.binding.Revision, Message: &m, Status: "pending", Created: time.Now().Unix()})
		added = true
	}
	if !added {
		return nil
	}
	if err := j.save(next); err != nil {
		return err
	}
	j.signal()
	return nil
}

func (j *Journal) AddHint(kind string, data json.RawMessage) error {
	if kind != "profile_review_due" && kind != "maintenance_due" && kind != "control_pending" {
		return errors.New("unsupported_dispatch_hint")
	}
	enabled := false
	for _, event := range j.binding.Events {
		if event == kind {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil
	}
	if len(data) > journalMaxBytes {
		return errors.New("hint_size_limit")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return errors.New("invalid_dispatch_hint")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	added := false
	if kind == "control_pending" {
		var ids []string
		if err := json.Unmarshal(obj["command_ids"], &ids); err != nil {
			return errors.New("invalid_control_command_ids")
		}
		seen := map[string]bool{}
		for _, job := range next.Jobs {
			seen[job.ID] = true
		}
		for _, command := range ids {
			if strings.TrimSpace(command) == "" {
				return errors.New("invalid_control_command_id")
			}
			id := journalID(j.binding.Scope, j.binding.Revision, kind, command)
			if seen[id] {
				continue
			}
			seen[id] = true
			singleton, _ := json.Marshal([]string{command})
			obj["command_ids"] = singleton
			payload, _ := json.Marshal(obj)
			next.Jobs = append(next.Jobs, j.newHint(id, kind, payload))
			added = true
		}
	} else {
		for _, job := range next.Jobs {
			if job.Kind == kind && !completedStatus(job.Status) {
				return nil
			}
		}
		next.Sequence++
		id := journalID(j.binding.Scope, j.binding.Revision, kind, fmt.Sprint(next.Sequence))
		next.Jobs = append(next.Jobs, j.newHint(id, kind, append(json.RawMessage(nil), data...)))
		added = true
	}
	if !added {
		return nil
	}
	if err := j.save(next); err != nil {
		return err
	}
	j.signal()
	return nil
}

func (j *Journal) newHint(id, kind string, data json.RawMessage) Job {
	return Job{ID: id, Kind: kind, Scope: j.binding.Scope, Revision: j.binding.Revision, Data: data, Status: "pending", Created: time.Now().Unix()}
}

func (j *Journal) Next() (Job, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	index := -1
	for i, job := range j.state.Jobs {
		if job.Status == "running" || job.Status == "sending" {
			return Job{}, false, nil
		}
		if job.Status == "pending" && (index < 0 || (job.Kind == "pm_push" && j.state.Jobs[index].Kind != "pm_push")) {
			index = i
		}
	}
	if index < 0 {
		return Job{}, false, nil
	}
	next := cloneJournal(j.state)
	next.Jobs[index].Status = "running"
	if err := j.save(next); err != nil {
		return Job{}, false, err
	}
	return cloneJob(j.state.Jobs[index]), true, nil
}

func (j *Journal) Update(id, status, code, sessionID, replyID string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		if !validTransition(job.Status, status) {
			return errors.New("invalid_job_transition")
		}
		if status == "replied" && replyID == "" {
			return errors.New("reply_id_required")
		}
		if job.Kind == "pm_push" && status == "completed" {
			return errors.New("pm_requires_explicit_decision")
		}
		job.Status = status
		job.Code = code
		if sessionID != "" {
			job.SessionID = sessionID
			if job.Message != nil {
				next.Sessions[job.Message.Conversation] = sessionID
			}
		}
		if replyID != "" {
			job.ReplyID = replyID
		}
		if completedStatus(status) {
			moveCompletedLast(&next, i)
		}
		if err := j.save(next); err != nil {
			return err
		}
		if j.hasPending() {
			j.signal()
		}
		return nil
	}
	return errors.New("job_not_found")
}

func validTransition(from, to string) bool {
	if !validJobStatus(to) {
		return false
	}
	switch from {
	case "running":
		return to == "sending" || to == "replied" || to == "no_reply" || to == "needs_user" || to == "failed" || to == "unknown" || to == "completed" || to == "accepted"
	case "sending":
		return to == "replied" || to == "failed" || to == "unknown" || to == "needs_user"
	}
	return false
}

func validJobStatus(s string) bool {
	switch s {
	case "pending", "running", "sending", "replied", "no_reply", "needs_user", "failed", "unknown", "completed", "accepted":
		return true
	}
	return false
}

func completedStatus(s string) bool {
	return s == "replied" || s == "no_reply" || s == "completed" || s == "accepted"
}

func unresolvedCount(s journalState) int {
	count := 0
	for _, job := range s.Jobs {
		if !completedStatus(job.Status) {
			count++
		}
	}
	return count
}

// Completion order, rather than arrival order, defines the deduplication window.
func moveCompletedLast(s *journalState, index int) {
	job := s.Jobs[index]
	s.Jobs = append(append(s.Jobs[:index], s.Jobs[index+1:]...), job)
}

func trimCompleted(s *journalState) {
	excess := len(s.Jobs) - unresolvedCount(*s) - journalMaxCompleted
	if excess <= 0 {
		return
	}
	kept := make([]Job, 0, len(s.Jobs)-excess)
	for _, job := range s.Jobs {
		if excess > 0 && completedStatus(job.Status) {
			excess--
			continue
		}
		kept = append(kept, job)
	}
	s.Jobs = kept
}

func cloneJob(job Job) Job {
	if job.Message != nil {
		m := *job.Message
		job.Message = &m
	}
	job.Data = append(json.RawMessage(nil), job.Data...)
	return job
}

func redactedJobs(jobs []Job) []Job {
	result := make([]Job, 0, len(jobs))
	for _, job := range jobs {
		job = cloneJob(job)
		job.Data = nil
		if job.Message != nil {
			job.Message.Content = ""
		}
		result = append(result, job)
	}
	return result
}

func (j *Journal) Snapshot() []Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return redactedJobs(j.state.Jobs)
}

func (j *Journal) Session(conversation string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state.Sessions[conversation]
}

func (j *Journal) Wake() <-chan struct{} { return j.wake }

func (j *Journal) signal() {
	select {
	case j.wake <- struct{}{}:
	default:
	}
}

func (j *Journal) hasPending() bool {
	for _, job := range j.state.Jobs {
		if job.Status == "pending" {
			return true
		}
	}
	return false
}

func (j *Journal) HasCapacity() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return unresolvedCount(j.state) < journalMaxPending
}

func (j *Journal) Retry(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		if job.Status != "failed" && job.Status != "needs_user" {
			return errors.New("job_not_safe_to_retry")
		}
		job.Status = "pending"
		job.Code = ""
		if err := j.save(next); err != nil {
			return err
		}
		j.signal()
		return nil
	}
	return errors.New("job_not_found")
}

// Reconcile records the operator's verified outcome. Callers must obtain explicit
// evidence before using this method; unknown jobs are never automatically retried.
func (j *Journal) Reconcile(id, action, replyID string) error {
	if (action != "no_reply" && action != "replied" && action != "completed" && action != "failed") || (action == "replied" && replyID == "") || (action != "replied" && replyID != "") {
		return errors.New("invalid_reconciliation")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		if job.Status != "unknown" && !(job.Kind != "pm_push" && job.Status == "needs_user") {
			return errors.New("job_not_reconcilable")
		}
		if (job.Kind == "pm_push") != (action == "no_reply" || action == "replied") {
			return errors.New("reconciliation_outcome_does_not_match_job_kind")
		}
		job.Status = action
		job.Code = "operator_verified"
		job.ReplyID = replyID
		if completedStatus(action) {
			moveCompletedLast(&next, i)
		}
		return j.save(next)
	}
	return errors.New("job_not_found")
}
