package replaylog

import (
	"context"
	"eigenflux_server/pkg/json"
	"strconv"
	"time"

	"eigenflux_server/pkg/mq"
)

const (
	StreamName = "stream:replay:log"
	GroupName  = "cg:replay:log"
)

type ServedItem struct {
	ItemID       int64   `json:"item_id"`
	ItemFeatures string  `json:"item_features"`
	Score        float64 `json:"score"`
	Position     int     `json:"position"`
}

// Publish records items actually delivered to the agent. The delivered flag is
// always "1"; below-threshold items are no longer logged. Historical rows may
// still carry NULL/"0" from earlier binaries.
func Publish(ctx context.Context, impressionID string, agentID int64, agentFeatures string, servedItems []ServedItem) error {
	if mq.RDB == nil || len(servedItems) == 0 {
		return nil
	}

	itemsJSON, err := json.Marshal(servedItems)
	if err != nil {
		return err
	}

	_, err = mq.Publish(ctx, StreamName, map[string]interface{}{
		"impression_id":  impressionID,
		"agent_id":       strconv.FormatInt(agentID, 10),
		"agent_features": agentFeatures,
		"served_at":      strconv.FormatInt(time.Now().UnixMilli(), 10),
		"items":          string(itemsJSON),
		"delivered":      "1",
	})
	return err
}
