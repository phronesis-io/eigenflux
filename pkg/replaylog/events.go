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

// delivered distinguishes items actually returned to the agent (true) from
// below-threshold items logged for offline analysis only (false).
func Publish(ctx context.Context, impressionID string, agentID int64, agentFeatures string, servedItems []ServedItem, delivered bool) error {
	if mq.RDB == nil || len(servedItems) == 0 {
		return nil
	}

	itemsJSON, err := json.Marshal(servedItems)
	if err != nil {
		return err
	}

	deliveredFlag := "0"
	if delivered {
		deliveredFlag = "1"
	}

	_, err = mq.Publish(ctx, StreamName, map[string]interface{}{
		"impression_id":  impressionID,
		"agent_id":       strconv.FormatInt(agentID, 10),
		"agent_features": agentFeatures,
		"served_at":      strconv.FormatInt(time.Now().UnixMilli(), 10),
		"items":          string(itemsJSON),
		"delivered":      deliveredFlag,
	})
	return err
}
