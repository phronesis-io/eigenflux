package feedevent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Ledger is an item_id -> Entry map built from the CLI's broadcast cache. It is
// the Go home of the plugin's feedback-ledger: the CLI already writes every feed
// response to <data>/broadcasts/<date>/feeds-*.json, so validation/enrichment
// reads that single source of truth instead of a per-plugin in-memory copy.
//
// impression_id is a PER-RESPONSE (top-level) field — items carry none — so an
// item is enriched with the impression of the MOST RECENT response that served
// it (newest file wins).
type Ledger struct {
	entries map[string]Entry
	exact   map[string]Entry
	pages   map[string]string
	files   int
}

// cachedResponse is the shape SaveFeedResponse persists (the API `data` object).
type cachedResponse struct {
	ImpressionID string `json:"impression_id"`
	Items        []struct {
		ItemID    string `json:"item_id"`
		SourceRef struct {
			Type string `json:"type"`
		} `json:"source_ref"`
		Summary string `json:"summary"`
		Preview struct {
			Text string `json:"text"`
		} `json:"preview"`
	} `json:"items"`
}

// NewLedger scans broadcastsDir newest-first and seeds the map. A missing
// directory yields an empty (all-missing) ledger — callers treat that as
// "unknown item", never an error.
func NewLedger(broadcastsDir, serverID string) *Ledger {
	l := &Ledger{entries: map[string]Entry{}, exact: map[string]Entry{}, pages: map[string]string{}}
	dateDirs, err := os.ReadDir(broadcastsDir)
	if err != nil {
		return l
	}
	// Date dirs are YYYYMMDD — lexical desc == newest first.
	names := make([]string, 0, len(dateDirs))
	for _, d := range dateDirs {
		if d.IsDir() {
			names = append(names, d.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	for _, date := range names {
		dir := filepath.Join(broadcastsDir, date)
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		feeds := make([]string, 0, len(files))
		for _, f := range files {
			if strings.HasPrefix(f.Name(), "feeds-") && strings.HasSuffix(f.Name(), ".json") {
				feeds = append(feeds, f.Name())
			}
		}
		// feeds-YYYYMMDD-HHmmss.json — lexical desc == newest first.
		sort.Sort(sort.Reverse(sort.StringSlice(feeds)))
		for _, name := range feeds {
			l.absorb(filepath.Join(dir, name), serverID)
		}
	}
	return l
}

func (l *Ledger) absorb(path, serverID string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var envelope struct {
		SchemaVersion   string `json:"schema_version"`
		Personalization struct {
			Mode            string `json:"mode"`
			ContextRevision int64  `json:"context_revision"`
		} `json:"personalization"`
	}
	_ = json.Unmarshal(data, &envelope)
	var resp cachedResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return
	}
	// Served time = the cache file's mtime (avoids parsing/timezone of the name).
	var servedMs int64
	if info, err := os.Stat(path); err == nil {
		servedMs = info.ModTime().UnixMilli()
	}
	order := l.files
	l.files++
	if _, exists := l.pages[resp.ImpressionID]; !exists {
		l.pages[resp.ImpressionID] = path
	}
	for position, it := range resp.Items {
		if it.ItemID == "" || (it.SourceRef.Type != "" && it.SourceRef.Type != "broadcast") {
			continue
		}

		if it.Summary == "" {
			it.Summary = it.Preview.Text
		}
		entry := Entry{
			ItemID: it.ItemID, ServedAt: servedMs, Feed: envelope.SchemaVersion == "feed.v2", Baseline: envelope.Personalization.Mode == "baseline",
			ImpressionID:    resp.ImpressionID,
			ServerID:        serverID,
			Title:           truncate(it.Summary, shortTitleMax),
			ExpiresAt:       servedMs + ledgerTTLMs,
			Position:        position,
			ContextRevision: envelope.Personalization.ContextRevision,
			CacheOrder:      order,
		}
		if _, seen := l.entries[it.ItemID]; !seen {
			l.entries[it.ItemID] = entry
		}
		if _, seen := l.exact[reportKey(resp.ImpressionID, it.ItemID)]; !seen {
			l.exact[reportKey(resp.ImpressionID, it.ItemID)] = entry
		}
	}
}

func (l *Ledger) Entries() []Entry {
	entries := make([]Entry, 0, len(l.exact))
	for _, entry := range l.exact {
		entries = append(entries, entry)
	}
	return entries
}

// Whole-page fallback remains scoreable even when its cache write failed.
func (l *Ledger) ReportEntries(reports Reports) []Entry {
	entries := l.Entries()
	for key, r := range reports.Entries {
		if _, cached := l.exact[key]; !cached && r.ServedAt > 0 {
			entries = append(entries, Entry{ItemID: r.ItemID, ImpressionID: r.ImpressionID, ServedAt: r.ServedAt, Feed: true, Baseline: r.Baseline, Position: r.Position})
		}
	}
	return entries
}

// Page reads the newest cached response for an impression.
func (l *Ledger) Page(impressionID string) (json.RawMessage, error) {
	path := l.pages[impressionID]
	if path == "" {
		return nil, fmt.Errorf("cached Feed page %q is unavailable", impressionID)
	}
	return os.ReadFile(path)
}

func (l *Ledger) LookupHanded(itemID string, reports Reports, now int64) (Entry, Status) {
	var latest Entry
	var handedAt int64
	for _, entry := range l.exact {
		r := reports.Exposure(entry)
		// Cache mtimes have millisecond precision; equal hand times fall back to
		// the newest cached response, as Lookup does.
		if entry.ItemID == itemID && entry.ExpiresAt >= now && r.HandedTime() > 0 &&
			(r.HandedTime() > handedAt || r.HandedTime() == handedAt && entry.CacheOrder < latest.CacheOrder) {
			latest, handedAt = entry, r.HandedTime()
		}
	}
	if handedAt == 0 {
		return Entry{}, StatusMissing
	}
	return latest, StatusHit
}

// Lookup returns the entry and whether the item is reportable at nowMs.
func (l *Ledger) Lookup(itemID string, nowMs int64) (Entry, Status) {
	e, ok := l.entries[itemID]
	if !ok {
		return Entry{}, StatusMissing
	}
	if e.ExpiresAt < nowMs {
		return e, StatusExpired
	}
	return e, StatusHit
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

func (l *Ledger) LookupImpression(itemID, impression string, nowMs int64) (Entry, Status) {
	if impression == "" {
		return l.Lookup(itemID, nowMs)
	}
	e, ok := l.exact[reportKey(impression, itemID)]
	if !ok {
		return Entry{}, StatusMissing
	}
	if e.ExpiresAt < nowMs {
		return e, StatusExpired
	}
	return e, StatusHit
}
