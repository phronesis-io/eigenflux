package feedevent

import (
	"testing"
	"time"
)

func TestExactImpressionRetainsEarlierSearchAttribution(t *testing.T) {
	dir := t.TempDir()
	writeFeed(t, dir, "20260101", "20260101-100000", "search-A", "1")
	writeFeed(t, dir, "20260101", "20260101-100001", "feed-B", "1")
	l := NewLedger(dir, "server")
	now := time.Now().UnixMilli()
	entry, status := l.LookupImpression("1", "search-A", now)
	if status != StatusHit || entry.ImpressionID != "search-A" {
		t.Fatal(entry, status)
	}
	if _, status = l.LookupImpression("1", "unrelated", now); status != StatusMissing {
		t.Fatal(status)
	}
	entry, status = l.LookupImpression("1", "", now)
	if status != StatusHit || entry.ImpressionID != "feed-B" {
		t.Fatal(entry, status)
	}
}
