package dal

import (
	"context"
	"errors"
	"sort"

	"console.eigenflux.ai/internal/db"
	"console.eigenflux.ai/internal/impr"
)

type AgentImprRecord struct {
	ItemIDs  []int64
	GroupIDs []int64
	URLs     []string
	Items    []ItemWithProcessed
}

func GetAgentImprRecord(ctx context.Context, agentID int64) (*AgentImprRecord, error) {
	if db.RDB == nil {
		return nil, errors.New("redis not initialized")
	}

	seen, err := impr.GetSeenItems(ctx, db.RDB, agentID)
	if err != nil {
		return nil, err
	}

	sort.Slice(seen.ItemIDs, func(i, j int) bool { return seen.ItemIDs[i] > seen.ItemIDs[j] })
	sort.Slice(seen.GroupIDs, func(i, j int) bool { return seen.GroupIDs[i] < seen.GroupIDs[j] })
	sort.Strings(seen.URLs)

	items, err := ListItemsByIDs(db.DB, seen.ItemIDs)
	if err != nil {
		return nil, err
	}

	return &AgentImprRecord{
		ItemIDs:  seen.ItemIDs,
		GroupIDs: seen.GroupIDs,
		URLs:     seen.URLs,
		Items:    items,
	}, nil
}

func ClearAgentImprRecord(ctx context.Context, agentID int64) error {
	if db.RDB == nil {
		return errors.New("redis not initialized")
	}
	return impr.ClearImpressions(ctx, db.RDB, agentID)
}
