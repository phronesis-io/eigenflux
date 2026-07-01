// Package feedevent owns the host-agnostic followup-event pipeline that used to
// live in the OpenClaw plugin (feedback-ledger + feedback-queue): validate an
// item_id against the CLI's own feed cache, enrich it with the impression that
// served it, and hold an on-disk queue that batches + flushes to the backend.
//
// The retry CADENCE stays with whoever has a resident process (the OpenClaw
// plugin's loop, a Codex automation task, cron) — they just call `flush`. The
// queue STATE and logic live here so every host reuses one implementation
// instead of re-writing it per adapter.
package feedevent

const (
	// QueueFileName is the on-disk queue under <data>/events/.
	QueueFileName = "queue.json"
	lockFileName  = ".queue.lock"

	// MaxBatch matches the backend/CLI push cap.
	MaxBatch = 50
	// MaxAgeMs drops events that never flushed within a day.
	MaxAgeMs = 24 * 60 * 60 * 1000
	// ledgerTTLMs mirrors the CLI's 8-day broadcast-cache retention: an item
	// last served longer ago than this is treated as expired for reporting.
	ledgerTTLMs   = 8 * 24 * 60 * 60 * 1000
	shortTitleMax = 80

	dirPerm         = 0o700
	filePerm        = 0o600
	staleLockSecond = 300
)

// Event is a backend event payload (already carrying its dedup_key). It is
// stored in the queue and POSTed verbatim, so the shape matches what the CLI's
// existing `feed event push` sends.
type Event = map[string]any

// Status is the outcome of validating an item_id against the feed cache.
type Status int

const (
	StatusHit Status = iota
	StatusExpired
	StatusMissing
)

func (s Status) String() string {
	switch s {
	case StatusHit:
		return "hit"
	case StatusExpired:
		return "expired"
	default:
		return "missing"
	}
}

// Entry is the cached metadata for one feed item, used to enrich an event.
type Entry struct {
	ImpressionID string
	ServerID     string
	Title        string
	ExpiresAt    int64 // epoch ms; served-time + ledgerTTL
}
