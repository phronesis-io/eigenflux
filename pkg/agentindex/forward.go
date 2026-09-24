package agentindex

import (
	"context"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

func Forward(rdb *redis.Client, index string) searchindex.Forward {
	return searchindex.Forward{Redis: rdb, Namespace: "agent:" + index}
}

func WriteForward(ctx context.Context, rdb *redis.Client, index string, d Document) error {
	return Forward(rdb, index).Put(ctx, d.AgentID, "card", d.ProjectionVersion, d)
}

func ReadForward(ctx context.Context, rdb *redis.Client, index string, ids []int64) (map[int64]Document, error) {
	rows, err := Forward(rdb, index).Get(ctx, ids, "card")
	if err != nil {
		return nil, err
	}
	out := map[int64]Document{}
	for id, raw := range rows {
		var d Document
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		if d.AgentID != id || d.Version <= 0 || d.ProjectionVersion <= 0 {
			return nil, fmt.Errorf("invalid Agent forward projection")
		}
		out[id] = d
	}
	return out, nil
}

// SearchFields excludes activity/freshness features. Embedding remains in ES
// for kNN, and in the forward document for exact cosine scoring after recall.
func (d Document) SearchFields() map[string]any {
	return map[string]any{"agent_id": d.AgentID, "version": d.Version, "projection_version": d.ProjectionVersion,
		"active": d.Active, "search_text": d.SearchText, "display_name": d.DisplayName,
		"retrieval_slots": d.Slots, "embedding": d.Embedding}
}
