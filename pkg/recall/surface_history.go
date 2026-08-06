package recall

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	SurfaceHistoryTTL      = 30 * 24 * time.Hour
	SurfaceHistoryMaxItems = 100
)

// SurfaceEvent is one confirmed downstream presentation of an item to an
// agent. ReportedAt is the client-reported Unix millisecond timestamp.
type SurfaceEvent struct {
	AgentID    int64
	ItemID     int64
	ReportedAt int64
}

// SurfaceHistoryStore owns the online Swing seed projection. PostgreSQL
// followup_labels remains the source of truth; this Redis ZSET is rebuildable.
type SurfaceHistoryStore struct {
	rdb       *redis.Client
	namespace string
	now       func() time.Time
}

func NewSurfaceHistoryStore(rdb *redis.Client, namespace string) *SurfaceHistoryStore {
	if namespace == "" {
		namespace = "rec"
	}
	return &SurfaceHistoryStore{rdb: rdb, namespace: namespace, now: time.Now}
}

func SurfaceHistoryKey(namespace string, agentID int64) string {
	if namespace == "" {
		namespace = "rec"
	}
	return fmt.Sprintf("%s:surface:agent:%d:items", namespace, agentID)
}

// Record inserts one surface event without allowing an older retry or backfill
// row to replace a newer timestamp for the same agent/item pair.
func (s *SurfaceHistoryStore) Record(ctx context.Context, event SurfaceEvent) error {
	return s.Upsert(ctx, []SurfaceEvent{event})
}

// Upsert merges surface events, prunes the 30-day window, and keeps the newest
// 100 items per agent. It is safe for live events and concurrent backfills.
func (s *SurfaceHistoryStore) Upsert(ctx context.Context, events []SurfaceEvent) error {
	if len(events) == 0 {
		return nil
	}
	if s == nil || s.rdb == nil {
		return fmt.Errorf("surface history: redis client is nil")
	}

	grouped := make(map[int64]map[int64]int64)
	for _, event := range events {
		if event.AgentID <= 0 || event.ItemID <= 0 || event.ReportedAt <= 0 {
			return fmt.Errorf("surface history: invalid event agent_id=%d item_id=%d reported_at=%d", event.AgentID, event.ItemID, event.ReportedAt)
		}
		items := grouped[event.AgentID]
		if items == nil {
			items = make(map[int64]int64)
			grouped[event.AgentID] = items
		}
		if event.ReportedAt > items[event.ItemID] {
			items[event.ItemID] = event.ReportedAt
		}
	}

	cutoff := s.now().Add(-SurfaceHistoryTTL).UnixMilli()
	pipe := s.rdb.TxPipeline()
	for agentID, items := range grouped {
		key := SurfaceHistoryKey(s.namespace, agentID)
		members := make([]redis.Z, 0, len(items))
		for itemID, reportedAt := range items {
			members = append(members, redis.Z{Score: float64(reportedAt), Member: strconv.FormatInt(itemID, 10)})
		}
		pipe.ZAddArgs(ctx, key, redis.ZAddArgs{GT: true, Members: members})
		pipe.ZRemRangeByScore(ctx, key, "-inf", "("+strconv.FormatInt(cutoff, 10))
		pipe.ZRemRangeByRank(ctx, key, 0, -SurfaceHistoryMaxItems-1)
		pipe.Expire(ctx, key, SurfaceHistoryTTL)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("surface history: upsert: %w", err)
	}
	return nil
}

// Recent returns confirmed surface item IDs ordered by reported_at descending.
// A missing key is a valid empty history and never falls back to impressions.
func (s *SurfaceHistoryStore) Recent(ctx context.Context, agentID int64, limit int) ([]int64, error) {
	if limit <= 0 {
		return nil, nil
	}
	if s == nil || s.rdb == nil {
		return nil, fmt.Errorf("surface history: redis client is nil")
	}
	if agentID <= 0 {
		return nil, fmt.Errorf("surface history: invalid agent_id=%d", agentID)
	}

	now := s.now()
	cutoff := now.Add(-SurfaceHistoryTTL).UnixMilli()
	key := SurfaceHistoryKey(s.namespace, agentID)
	pipe := s.rdb.TxPipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", "("+strconv.FormatInt(cutoff, 10))
	itemsCmd := pipe.ZRevRangeByScore(ctx, key, &redis.ZRangeBy{
		Max:    strconv.FormatInt(now.UnixMilli(), 10),
		Min:    strconv.FormatInt(cutoff, 10),
		Offset: 0,
		Count:  int64(limit),
	})
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("surface history: recent: %w", err)
	}

	values := itemsCmd.Val()
	itemIDs := make([]int64, 0, len(values))
	for _, value := range values {
		itemID, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("surface history: invalid item id %q: %w", value, err)
		}
		itemIDs = append(itemIDs, itemID)
	}
	return itemIDs, nil
}
