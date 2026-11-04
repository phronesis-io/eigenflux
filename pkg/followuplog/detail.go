package followuplog

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
	"unicode"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
)

const DetailFetch = "detail_fetch"

// RecordDetailFetch records successful non-author detail reads. A read is an
// Agent inspection signal, never evidence of human attention or a positive label.
func RecordDetailFetch(ctx context.Context, agentID, authorID, itemID int64, impression string) {
	if mq.RDB == nil || agentID <= 0 || itemID <= 0 || agentID == authorID {
		return
	}
	event := detailEvent(agentID, itemID, impression, time.Now())
	base := context.WithoutCancel(ctx)
	go func() {
		publishCtx, cancel := context.WithTimeout(base, 3*time.Second)
		defer cancel()
		if err := Publish(publishCtx, []Event{event}); err != nil {
			logger.Ctx(publishCtx).Warn("Feed detail signal publish failed", "agent_id", agentID, "item_id", itemID, "err", err)
		}
	}()
}

func detailEvent(agentID, itemID int64, impression string, now time.Time) Event {
	impression = strings.TrimSpace(impression)
	if len(impression) > 64 || strings.ContainsFunc(impression, unicode.IsControl) {
		impression = ""
	}
	// Retries count once per exposure. Older callers without attribution count
	// once per UTC day and must stay outside exposure-based training labels.
	identity := "impression:" + impression
	if impression == "" {
		identity = "unattributed:" + now.UTC().Format("2006-01-02")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("server:%s:%d:%d:%s", DetailFetch, agentID, itemID, identity)))
	return Event{AgentID: agentID, ItemID: itemID, Kind: DetailFetch, ImpressionID: impression,
		DedupKey: fmt.Sprintf("%x", digest[:16]), ReportedAt: now.UnixMilli()}
}
