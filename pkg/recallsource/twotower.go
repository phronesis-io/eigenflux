package recallsource

import (
	"context"
	"fmt"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/recall"
)

const defaultTwoTowerRecallKey = "two_tower_recall"

// TwoTowerRecallSource reads precomputed two-tower user candidates from Redis.
type TwoTowerRecallSource struct {
	reader *recall.RedisRecallReader
	key    string
	k      int
}

func NewTwoTowerRecallSource(reader *recall.RedisRecallReader, key string, k int) *TwoTowerRecallSource {
	if key == "" {
		key = defaultTwoTowerRecallKey
	}
	if k <= 0 {
		k = 50
	}
	return &TwoTowerRecallSource{reader: reader, key: key, k: k}
}

func (t *TwoTowerRecallSource) Name() string       { return "two_tower" }
func (t *TwoTowerRecallSource) SourceFlag() Source { return TwoTower }

func (t *TwoTowerRecallSource) Recall(ctx context.Context, userID string, limit int) ([]Candidate, error) {
	if t.reader == nil {
		return nil, fmt.Errorf("two_tower: recall reader is nil")
	}

	scored, err := t.reader.FetchUserScoredCandidates(ctx, t.key, userID)
	if err != nil {
		return nil, fmt.Errorf("two_tower: fetch user candidates: %w", err)
	}

	k := t.k
	if limit > 0 && limit < k {
		k = limit
	}
	if len(scored) > k {
		scored = scored[:k]
	}

	candidates := make([]Candidate, 0, len(scored))
	for _, c := range scored {
		candidates = append(candidates, Candidate{
			ItemID: c.ItemID,
			Score:  c.Score,
			Source: TwoTower,
		})
	}

	logger.Ctx(ctx).Debug("two_tower recall", "userID", userID, "key", t.key, "k", k, "returned", len(candidates))
	return candidates, nil
}
