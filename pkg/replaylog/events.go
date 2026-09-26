package replaylog

import (
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"strconv"
	"time"

	"eigenflux_server/pkg/mq"
)

const (
	StreamName = "stream:replay:log"
	GroupName  = "cg:replay:log"
)

type ServedItem struct {
	NeedID              int64   `json:"need_id,omitempty"`
	PipelineVersion     string  `json:"pipeline_version,omitempty"`
	RequestMode         string  `json:"request_mode,omitempty"`
	SampleSchemaVersion int     `json:"sample_schema_version,omitempty"`
	SourceKind          string  `json:"source_kind,omitempty"`
	SourceID            int64   `json:"source_id,omitempty"`
	ContextID           int64   `json:"context_id,omitempty"`
	NeedRevision        int64   `json:"need_revision,omitempty"`
	ItemID              int64   `json:"item_id"`
	ItemFeatures        string  `json:"item_features"`
	Score               float64 `json:"score"`
	Position            int     `json:"position"`
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

// Validate rejects mixed identities before a typed event reaches the shared table.
func (s ServedItem) Validate() error {
	if s.PipelineVersion == "" || s.PipelineVersion == "legacy_feed_v1" {
		if s.ItemID <= 0 || s.SourceKind != "" && s.SourceKind != "broadcast" || s.SourceID != 0 && s.SourceID != s.ItemID {
			return fmt.Errorf("invalid legacy replay identity")
		}
		return nil
	}
	if s.PipelineVersion != "need_search_v1" || s.SampleSchemaVersion != 2 || s.ContextID <= 0 || s.SourceID <= 0 || s.RequestMode != "search" && s.RequestMode != "recommendation" {
		return fmt.Errorf("invalid discovery sample metadata")
	}
	if s.NeedID < 0 || s.NeedRevision < 0 || (s.NeedID == 0) != (s.NeedRevision == 0) {
		return fmt.Errorf("invalid Need revision metadata")
	}
	switch s.SourceKind {
	case "broadcast":
		if s.ItemID != s.SourceID {
			return fmt.Errorf("broadcast identity mismatch")
		}
	case "commission", "agent":
		if s.ItemID != 0 {
			return fmt.Errorf("typed source in item_id")
		}
	default:
		return fmt.Errorf("unknown source kind")
	}
	return nil
}
