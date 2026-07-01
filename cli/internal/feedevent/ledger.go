package feedevent

import (
	"encoding/json"
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
}

// cachedResponse is the shape SaveFeedResponse persists (the API `data` object).
type cachedResponse struct {
	ImpressionID string `json:"impression_id"`
	Items        []struct {
		ItemID  string `json:"item_id"`
		Summary string `json:"summary"`
	} `json:"items"`
}

// NewLedger scans broadcastsDir newest-first and seeds the map. A missing
// directory yields an empty (all-missing) ledger — callers treat that as
// "unknown item", never an error.
func NewLedger(broadcastsDir, serverID string) *Ledger {
	l := &Ledger{entries: map[string]Entry{}}
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
	var resp cachedResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return
	}
	// Served time = the cache file's mtime (avoids parsing/timezone of the name).
	var servedMs int64
	if info, err := os.Stat(path); err == nil {
		servedMs = info.ModTime().UnixMilli()
	}
	for _, it := range resp.Items {
		if it.ItemID == "" {
			continue
		}
		if _, seen := l.entries[it.ItemID]; seen {
			continue // newest response already recorded this item
		}
		l.entries[it.ItemID] = Entry{
			ImpressionID: resp.ImpressionID,
			ServerID:     serverID,
			Title:        truncate(it.Summary, shortTitleMax),
			ExpiresAt:    servedMs + ledgerTTLMs,
		}
	}
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
	if len(s) <= max {
		return s
	}
	return s[:max]
}
