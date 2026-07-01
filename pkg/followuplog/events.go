package followuplog

import (
	"context"
	"strconv"

	"eigenflux_server/pkg/mq"
)

const (
	StreamName = "stream:followup:label"
	GroupName  = "cg:followup:label"
)

// Event is one reported follow-up behavior. AgentID is authoritative (set from
// the request bearer token), never from the client body.
type Event struct {
	AgentID      int64
	ItemID       int64
	Kind         string
	ImpressionID string
	Brief        string
	SessionKey   string
	Channel      string
	ServerID     string
	DedupKey     string
	ReportedAt   int64
}

func Publish(ctx context.Context, events []Event) error {
	if mq.RDB == nil || len(events) == 0 {
		return nil
	}
	for _, e := range events {
		if _, err := mq.Publish(ctx, StreamName, map[string]interface{}{
			"agent_id":      strconv.FormatInt(e.AgentID, 10),
			"item_id":       strconv.FormatInt(e.ItemID, 10),
			"kind":          e.Kind,
			"impression_id": e.ImpressionID,
			"brief":         e.Brief,
			"session_key":   e.SessionKey,
			"channel":       e.Channel,
			"server_id":     e.ServerID,
			"dedup_key":     e.DedupKey,
			"reported_at":   strconv.FormatInt(e.ReportedAt, 10),
		}); err != nil {
			return err
		}
	}
	return nil
}
