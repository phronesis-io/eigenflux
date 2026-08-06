package recallsource

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/recall"

	"github.com/redis/go-redis/v9"
)

const defaultSwingI2IRecallKey = "swing_i2i"

// SwingI2IRecallSource expands an agent's latest confirmed surface items through
// the offline Swing item-to-item neighbor index and sums scores across seeds.
type SwingI2IRecallSource struct {
	reader         *recall.RedisRecallReader
	surfaceHistory *recall.SurfaceHistoryStore
	rdb            *redis.Client
	seedLimit      int
	k              int
}

func NewSwingI2IRecallSource(reader *recall.RedisRecallReader, surfaceHistory *recall.SurfaceHistoryStore, rdb *redis.Client, seedLimit, k int) *SwingI2IRecallSource {
	if seedLimit <= 0 {
		seedLimit = 20
	}
	if k <= 0 {
		k = 100
	}
	return &SwingI2IRecallSource{reader: reader, surfaceHistory: surfaceHistory, rdb: rdb, seedLimit: seedLimit, k: k}
}

func (s *SwingI2IRecallSource) Name() string       { return "swing_i2i" }
func (s *SwingI2IRecallSource) SourceFlag() Source { return SwingI2I }

func (s *SwingI2IRecallSource) Recall(ctx context.Context, userID string, limit int) ([]Candidate, error) {
	if s.reader == nil {
		return nil, fmt.Errorf("swing_i2i: recall reader is nil")
	}
	if s.rdb == nil {
		return nil, fmt.Errorf("swing_i2i: redis client is nil")
	}
	if s.surfaceHistory == nil {
		return nil, fmt.Errorf("swing_i2i: surface history is nil")
	}
	agentID, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("swing_i2i: invalid user id %q: %w", userID, err)
	}

	seedIDs, err := s.surfaceHistory.Recent(ctx, agentID, s.seedLimit)
	if err != nil {
		return nil, fmt.Errorf("swing_i2i: fetch surface seeds: %w", err)
	}
	if len(seedIDs) == 0 {
		return nil, nil
	}

	seenItemIDs, err := impr.GetSeenItemIDs(ctx, s.rdb, agentID)
	if err != nil {
		return nil, fmt.Errorf("swing_i2i: fetch seen items: %w", err)
	}

	seenIDs := make(map[int64]struct{}, len(seenItemIDs))
	for _, itemID := range seenItemIDs {
		seenIDs[itemID] = struct{}{}
	}
	for _, itemID := range seedIDs {
		seenIDs[itemID] = struct{}{}
	}

	neighborsBySeed, err := s.reader.FetchItemScoredNeighborsBatch(ctx, defaultSwingI2IRecallKey, seedIDs)
	if err != nil {
		return nil, fmt.Errorf("swing_i2i: fetch neighbors: %w", err)
	}
	aggregated := make(map[int64]float64)
	for _, seedID := range seedIDs {
		for _, neighbor := range neighborsBySeed[seedID] {
			if _, alreadySeen := seenIDs[neighbor.ItemID]; alreadySeen {
				continue
			}
			aggregated[neighbor.ItemID] += neighbor.Score
		}
	}

	candidates := make([]Candidate, 0, len(aggregated))
	for itemID, score := range aggregated {
		candidates = append(candidates, Candidate{ItemID: itemID, Score: score, Source: SwingI2I})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].ItemID > candidates[j].ItemID
		}
		return candidates[i].Score > candidates[j].Score
	})

	k := s.k
	if limit > 0 && limit < k {
		k = limit
	}
	if len(candidates) > k {
		candidates = candidates[:k]
	}
	logger.Ctx(ctx).Debug("swing_i2i recall", "userID", userID, "seeds", len(seedIDs), "returned", len(candidates))
	return candidates, nil
}
