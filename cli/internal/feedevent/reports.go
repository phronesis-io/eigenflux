package feedevent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const PendingWindowMs int64 = 48 * 60 * 60 * 1000
const HealthWindowMs int64 = 24 * 60 * 60 * 1000
const FeedbackContractVersion = "feedback.v2"

type Report struct {
	ImpressionID      string `json:"impression_id"`
	ItemID            string `json:"item_id"`
	ServedAt          int64  `json:"served_at"`
	ScoredAt          int64  `json:"scored_at,omitempty"`
	Score             int    `json:"score,omitempty"`
	ClientKey         string `json:"client_key,omitempty"`
	Delivery          string `json:"delivery,omitempty"`
	SurfaceReportedAt int64  `json:"surface_reported_at,omitempty"`
	Baseline          bool   `json:"baseline"`
}
type HealthEvent struct {
	At     int64            `json:"at"`
	Counts map[string]int64 `json:"counts"`
}
type Reports struct {
	Scope   string            `json:"scope"`
	Entries map[string]Report `json:"entries"`
	Health  []HealthEvent     `json:"health,omitempty"`
}

// ErrCorruptReports marks an unreadable ledger file.
var ErrCorruptReports = errors.New("feedevent: corrupt report ledger")

type ReportStore struct {
	dir, scope string
	wait       time.Duration
}

func NewReportStore(dir, scope string) *ReportStore {
	return &ReportStore{dir: dir, scope: scope, wait: 2 * time.Second}
}
func reportKey(impression, item string) string { return impression + "\x00" + item }
func (s *ReportStore) Read() (Reports, error) {
	state := Reports{Scope: s.scope, Entries: map[string]Report{}}
	data, err := os.ReadFile(filepath.Join(s.dir, "feed-reports.json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return Reports{Scope: s.scope, Entries: map[string]Report{}}, fmt.Errorf("%w: %v", ErrCorruptReports, err)
	}
	if state.Scope != s.scope {
		return Reports{Scope: s.scope, Entries: map[string]Report{}}, nil
	}
	if state.Entries == nil {
		state.Entries = map[string]Report{}
	}
	return state, nil
}
func (s *ReportStore) Update(now int64, fn func(*Reports)) error {
	l, ok := acquireNamedLockFor(s.dir, ".feed-reports.lock", s.wait)
	if !ok {
		return fmt.Errorf("feedevent: report ledger busy")
	}
	defer l.release()
	state, err := s.Read()
	if errors.Is(err, ErrCorruptReports) {
		// The ledger is rebuilt from later pulls and judgments. Keep the
		// unreadable copy for diagnosis instead of overwriting it.
		corrupt := filepath.Join(s.dir, fmt.Sprintf("feed-reports.json.corrupt-%d", now))
		if renameErr := os.Rename(filepath.Join(s.dir, "feed-reports.json"), corrupt); renameErr != nil {
			return err
		}
		err = nil
	}
	if err != nil {
		return err
	}
	for key, entry := range state.Entries {
		if max(entry.ServedAt, entry.ScoredAt, entry.SurfaceReportedAt) < now-ledgerTTLMs {
			delete(state.Entries, key)
		}
	}
	fresh := state.Health[:0]
	for _, e := range state.Health {
		if e.At > now-HealthWindowMs && e.At <= now {
			fresh = append(fresh, e)
		}
	}
	state.Health = fresh
	fn(&state)
	return writeAtomicJSON(s.dir, "feed-reports.json", state)
}
func writeAtomicJSON(dir, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".feed-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
func (r *Reports) Count(now int64, counts map[string]int64) {
	r.Health = append(r.Health, HealthEvent{At: now, Counts: counts})
}
func (r Reports) HealthCounts(now int64) map[string]int64 {
	result := map[string]int64{}
	for _, e := range r.Health {
		if e.At > now-HealthWindowMs && e.At <= now {
			for k, v := range e.Counts {
				result[k] += v
			}
		}
	}
	return result
}
func (s *ReportStore) Count(now int64, counts map[string]int64) error {
	return s.Update(now, func(r *Reports) { r.Count(now, counts) })
}
func (r *Reports) RecordFeedback(item Event, now int64, delivery string) {
	imp, _ := item["impression_id"].(string)
	id, _ := item["item_id"].(string)
	key := reportKey(imp, id)
	entry := r.Entries[key]
	entry.ImpressionID = imp
	entry.ItemID = id
	clientKey, _ := item["client_key"].(string)
	judgedAt := asInt64(item["ts"])
	if judgedAt <= 0 {
		judgedAt = now
	}
	// Reconstruct the judgment from the durable event if the writer stopped
	// between queue persistence and ledger persistence. Older receipts cannot
	// replace a newer judgment already recorded by another invocation.
	if entry.ClientKey != clientKey && entry.ClientKey != "" &&
		(judgedAt < entry.ScoredAt || delivery != "queued" && judgedAt == entry.ScoredAt) {
		return
	}
	if entry.ClientKey == clientKey && entry.Delivery != "" {
		if delivery == "queued" {
			return
		}
	} else {
		entry.ScoredAt = judgedAt
		entry.Score = int(asInt64(item["score"]))
		entry.ClientKey = clientKey
		r.Count(judgedAt, map[string]int64{"scored_items": 1})
	}
	entry.Delivery = delivery
	r.Entries[key] = entry
}
func (r *Reports) RecordSurface(item Event, now int64) {
	imp, _ := item["impression_id"].(string)
	id, _ := item["item_id"].(string)
	key := reportKey(imp, id)
	entry := r.Entries[key]
	entry.ImpressionID = imp
	entry.ItemID = id
	entry.SurfaceReportedAt = now
	r.Entries[key] = entry
}
func FeedbackKey(scope, impression, item string, score int, surfaced *bool) string {
	declaration := "unknown"
	if surfaced != nil {
		declaration = fmt.Sprint(*surfaced)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s", scope, impression, item, score, declaration)))
	return hex.EncodeToString(sum[:])[:32]
}

type PendingItem struct {
	ItemID          string `json:"item_id"`
	ImpressionID    string `json:"impression_id"`
	AgeMs           int64  `json:"age_ms"`
	Title           string `json:"title"`
	AlreadySurfaced bool   `json:"already_surfaced"`
}

func (l *Ledger) Pending(reports Reports, now int64) []PendingItem {
	pending := []PendingItem{}
	surfaced := map[string]bool{}
	for _, r := range reports.Entries {
		if r.SurfaceReportedAt > now-ledgerTTLMs {
			surfaced[r.ItemID] = true
		}
	}
	for key, e := range l.exact {
		if !e.Feed || e.Baseline || e.ServedAt <= now-PendingWindowMs || e.ServedAt > now {
			continue
		}
		report := reports.Entries[key]
		if report.Baseline || report.Delivery == "queued" || report.Delivery == "accepted" || report.Delivery == "duplicate" || report.Delivery == "rejected" {
			continue
		}
		pending = append(pending, PendingItem{ItemID: e.ItemID, ImpressionID: e.ImpressionID, AgeMs: now - e.ServedAt, Title: e.Title, AlreadySurfaced: surfaced[e.ItemID]})
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].AgeMs != pending[j].AgeMs {
			return pending[i].AgeMs > pending[j].AgeMs
		}
		if pending[i].ImpressionID != pending[j].ImpressionID {
			return pending[i].ImpressionID < pending[j].ImpressionID
		}
		return pending[i].ItemID < pending[j].ItemID
	})
	return pending
}

func (s *ReportStore) WithLockWait(wait time.Duration) *ReportStore { s.wait = wait; return s }
