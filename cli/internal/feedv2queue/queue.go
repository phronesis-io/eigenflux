// Package feedv2queue persists leased Feed V2 batches before they are exposed
// to a Runtime. The queue is deliberately small and single-consumer: network
// fetching can retry, but one local Agent session processes one entry at a time.
package feedv2queue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const MaxEntries = 8

type Entry struct {
	BatchID      string           `json:"batch_id"`
	LeaseEpoch   int64            `json:"lease_epoch"`
	LeaseToken   string           `json:"lease_token"`
	LeaseUntil   int64            `json:"lease_until,omitempty"`
	EnqueuedAt   int64            `json:"enqueued_at"`
	StaleAt      int64            `json:"stale_at,omitempty"`
	StaleReason  string           `json:"stale_reason,omitempty"`
	CardVersions map[string]int64 `json:"card_versions,omitempty"`
	Payload      json.RawMessage  `json:"payload"`
}

type state struct {
	Version           int              `json:"version"`
	Entries           []Entry          `json:"entries"`
	Stale             []Entry          `json:"stale,omitempty"`
	KnownCardVersions map[string]int64 `json:"known_public_card_versions,omitempty"`
}

type Queue struct {
	dir      string
	path     string
	lockPath string
}

func New(dir string) *Queue {
	return &Queue{dir: dir, path: filepath.Join(dir, "queue.json"), lockPath: filepath.Join(dir, ".queue.lock")}
}

func (q *Queue) load() (state, error) {
	data, err := os.ReadFile(q.path)
	if os.IsNotExist(err) {
		return state{Version: 1, KnownCardVersions: map[string]int64{}}, nil
	}
	if err != nil {
		return state{}, err
	}
	var stored state
	if json.Unmarshal(data, &stored) != nil || stored.Version != 1 {
		return state{}, fmt.Errorf("Feed V2 queue is corrupt; preserve %s for recovery", q.path)
	}
	if stored.KnownCardVersions == nil {
		stored.KnownCardVersions = map[string]int64{}
	}
	return stored, nil
}

func (q *Queue) save(stored state) error {
	if err := os.MkdirAll(q.dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(q.dir, ".queue-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, q.path)
}

func (q *Queue) withLock(fn func() error) error {
	if err := os.MkdirAll(q.dir, 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(q.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil && os.IsExist(err) {
		if info, statErr := os.Stat(q.lockPath); statErr == nil && time.Since(info.ModTime()) > 5*time.Minute {
			_ = os.Remove(q.lockPath)
			file, err = os.OpenFile(q.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		}
	}
	if err != nil {
		return fmt.Errorf("Feed V2 queue is busy")
	}
	_, _ = file.WriteString(strconv.Itoa(os.Getpid()))
	_ = file.Sync()
	defer func() {
		_ = file.Close()
		_ = os.Remove(q.lockPath)
	}()
	return fn()
}

func parseEntry(payload json.RawMessage) (Entry, map[string]int64, error) {
	var envelope struct {
		BatchID string `json:"batch_id"`
		Lease   struct {
			Epoch     int64  `json:"epoch"`
			Token     string `json:"token"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"lease"`
		CardUpdates map[string]struct {
			Version int64 `json:"public_card_version"`
		} `json:"agent_card_updates"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.BatchID == "" || envelope.Lease.Epoch <= 0 || envelope.Lease.Token == "" {
		return Entry{}, nil, fmt.Errorf("invalid Feed V2 batch payload")
	}
	versions := make(map[string]int64, len(envelope.CardUpdates))
	for id, update := range envelope.CardUpdates {
		if update.Version > 0 {
			versions[id] = update.Version
		}
	}
	return Entry{BatchID: envelope.BatchID, LeaseEpoch: envelope.Lease.Epoch,
		LeaseToken: envelope.Lease.Token, LeaseUntil: envelope.Lease.ExpiresAt,
		EnqueuedAt: time.Now().UnixMilli(), CardVersions: versions, Payload: payload}, versions, nil
}

func replaceLeaseExpiry(payload json.RawMessage, leaseUntil int64) json.RawMessage {
	var envelope map[string]interface{}
	if json.Unmarshal(payload, &envelope) != nil {
		return payload
	}
	lease, _ := envelope["lease"].(map[string]interface{})
	if lease == nil {
		return payload
	}
	lease["expires_at"] = leaseUntil
	updated, err := json.Marshal(envelope)
	if err != nil {
		return payload
	}
	return updated
}

func (q *Queue) Enqueue(payload json.RawMessage) (int, error) {
	entry, _, err := parseEntry(payload)
	if err != nil {
		return 0, err
	}
	depth := 0
	err = q.withLock(func() error {
		stored, err := q.load()
		if err != nil {
			return err
		}
		replaced := false
		for index := range stored.Entries {
			if stored.Entries[index].BatchID == entry.BatchID {
				stored.Entries[index] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			if len(stored.Entries) >= MaxEntries {
				return fmt.Errorf("Feed V2 queue reached its %d-batch safety limit", MaxEntries)
			}
			stored.Entries = append(stored.Entries, entry)
		}
		// Known Card versions are committed only after a successful server ACK.
		// A queued or fenced batch has not been processed by the Agent yet.
		depth = len(stored.Entries)
		return q.save(stored)
	})
	return depth, err
}

func (q *Queue) Snapshot() ([]Entry, map[string]int64, error) {
	var entries []Entry
	versions := map[string]int64{}
	err := q.withLock(func() error {
		stored, err := q.load()
		if err != nil {
			return err
		}
		entries = append(entries, stored.Entries...)
		for id, version := range stored.KnownCardVersions {
			versions[id] = version
		}
		return nil
	})
	return entries, versions, err
}

func (q *Queue) StaleSnapshot() ([]Entry, error) {
	var entries []Entry
	err := q.withLock(func() error {
		stored, err := q.load()
		if err != nil {
			return err
		}
		entries = append(entries, stored.Stale...)
		return nil
	})
	return entries, err
}

// Renew updates the durable expiry only after the server accepted the current
// fencing proof. The local queue lock keeps heartbeat, poll, and ack processes
// from racing the replacement.
func (q *Queue) Renew(batchID string, push func(Entry) (int64, error)) (Entry, error) {
	batchID = strings.TrimSpace(batchID)
	var renewed Entry
	err := q.withLock(func() error {
		stored, err := q.load()
		if err != nil {
			return err
		}
		index := -1
		for i := range stored.Entries {
			if batchID == "" || stored.Entries[i].BatchID == batchID {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("Feed V2 batch %q is not queued", batchID)
		}
		leaseUntil, err := push(stored.Entries[index])
		if err != nil {
			return err
		}
		stored.Entries[index].LeaseUntil = leaseUntil
		stored.Entries[index].Payload = replaceLeaseExpiry(stored.Entries[index].Payload, leaseUntil)
		renewed = stored.Entries[index]
		return q.save(stored)
	})
	return renewed, err
}

// MoveToStale removes an irrecoverably fenced entry from the active queue but
// preserves a small forensic record. It never reports the batch as processed.
func (q *Queue) MoveToStale(batchID, reason string) (int, error) {
	batchID = strings.TrimSpace(batchID)
	remaining := 0
	err := q.withLock(func() error {
		stored, err := q.load()
		if err != nil {
			return err
		}
		index := -1
		for i := range stored.Entries {
			if batchID == "" || stored.Entries[i].BatchID == batchID {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("Feed V2 batch %q is not queued", batchID)
		}
		entry := stored.Entries[index]
		entry.StaleAt = time.Now().UnixMilli()
		entry.StaleReason = reason
		stored.Entries = append(stored.Entries[:index], stored.Entries[index+1:]...)
		stored.Stale = append(stored.Stale, entry)
		if len(stored.Stale) > MaxEntries {
			stored.Stale = stored.Stale[len(stored.Stale)-MaxEntries:]
		}
		remaining = len(stored.Entries)
		return q.save(stored)
	})
	return remaining, err
}

func (q *Queue) Acknowledge(batchID string, push func(Entry) error) (int, error) {
	batchID = strings.TrimSpace(batchID)
	remaining := 0
	err := q.withLock(func() error {
		stored, err := q.load()
		if err != nil {
			return err
		}
		index := -1
		for i := range stored.Entries {
			if batchID == "" || stored.Entries[i].BatchID == batchID {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("Feed V2 batch %q is not queued", batchID)
		}
		if err := push(stored.Entries[index]); err != nil {
			return err
		}
		for id, version := range stored.Entries[index].CardVersions {
			if version > stored.KnownCardVersions[id] {
				stored.KnownCardVersions[id] = version
			}
		}
		stored.Entries = append(stored.Entries[:index], stored.Entries[index+1:]...)
		remaining = len(stored.Entries)
		return q.save(stored)
	})
	return remaining, err
}
