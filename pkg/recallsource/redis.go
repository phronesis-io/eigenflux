package recallsource

import (
	"context"

	"eigenflux_server/pkg/recall"
)

// RedisRecallSource implements RecallSource for Redis-based recall indices
// (hot_recall, new_recall). It reads item ID lists from Redis via RedisRecallReader.
type RedisRecallSource struct {
	reader   *recall.RedisRecallReader
	key      string // e.g. "hot_recall", "new_recall"
	source   Source
	nameStr  string
}

func NewRedisRecallSource(reader *recall.RedisRecallReader, key string, source Source, name string) *RedisRecallSource {
	return &RedisRecallSource{
		reader:  reader,
		key:     key,
		source:  source,
		nameStr: name,
	}
}

func (r *RedisRecallSource) Name() string       { return r.nameStr }
func (r *RedisRecallSource) SourceFlag() Source  { return r.source }

func (r *RedisRecallSource) Recall(ctx context.Context, userID string, limit int) ([]Candidate, error) {
	ids, err := r.reader.FetchItemIDIndex(ctx, r.key)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	candidates := make([]Candidate, len(ids))
	for i, id := range ids {
		candidates[i] = Candidate{ItemID: id, Source: r.source}
	}
	return candidates, nil
}
