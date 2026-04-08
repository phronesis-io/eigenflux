package replaylog

import (
	"context"
	"encoding/json"
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
	})
	return err
}
