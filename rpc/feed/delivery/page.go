package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"eigenflux_server/rpc/sort/discovery"

	"github.com/redis/go-redis/v9"
)

type pageState struct {
	Impression string              `json:"impression_id"`
	Position   int                 `json:"position"`
	Execution  discovery.Execution `json:"execution"`
}

// ServePage keeps a frozen ranking and advances the cursor independently of
// best-effort exposure recording. prepare assembles each page from current details.
func (s Service) ServePage(ctx context.Context, owner int64, action string, limit int, prepare func(context.Context, *discovery.Execution) error) (discovery.Response, bool, error) {
	empty := discovery.Response{Mode: discovery.Recommendation, PipelineVersion: discovery.PipelineVersion, Status: "exhausted", Items: []discovery.ResultItem{}}
	if owner <= 0 {
		return empty, false, discovery.Failure(401, "unauthorized")
	}
	if action != "refresh" && action != "load_more" {
		return empty, false, discovery.Invalid("action", "invalid")
	}
	if limit < 1 || limit > 100 {
		return empty, false, discovery.Invalid("limit", "out_of_range")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, err := s.IDs.NextID()
	if err != nil {
		return empty, false, err
	}
	pageKey := fmt.Sprintf("discovery:feed:%d:page", owner)
	lockKey := pageKey + ":lock"
	lockToken := fmt.Sprint(token)
	unlock, err := s.lock(ctx, lockKey, lockToken)
	if err != nil {
		return empty, false, err
	}
	defer unlock()
	state := pageState{}
	if action == "load_more" {
		raw, err := s.Redis.Get(ctx, pageKey).Result()
		if err == redis.Nil {
			return empty, false, nil
		}
		if err != nil {
			return empty, false, err
		}
		if err = json.Unmarshal([]byte(raw), &state); err != nil {
			return empty, false, err
		}
		if len(state.Execution.Candidates) == 0 {
			empty.ImpressionID = state.Impression
			return empty, false, nil
		}
	} else {
		state.Impression = lockToken
		state.Execution, err = s.Executor.Execute(ctx, owner, discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}}, "legacy_prefetch", time.Now().UnixMilli())
		if err != nil {
			return empty, false, err
		}
	}
	selected := state.Execution
	selected.Mode = discovery.Recommendation
	selected.Candidates = nil
	for len(state.Execution.Candidates) > 0 && len(selected.Candidates) < limit {
		count := min(limit-len(selected.Candidates), len(state.Execution.Candidates))
		batch := state.Execution
		batch.Candidates = append([]discovery.Candidate(nil), state.Execution.Candidates[:count]...)
		if prepare != nil {
			if err := prepare(ctx, &batch); err != nil {
				return empty, false, err
			}
		}
		state.Execution.Candidates = state.Execution.Candidates[count:]
		selected.Candidates = append(selected.Candidates, batch.Candidates...)
	}
	if len(selected.Candidates) == 0 && selected.Status == "ok" {
		selected.Status = "exhausted"
	}
	position := state.Position
	state.Position += len(selected.Candidates)
	hasMore := len(state.Execution.Candidates) > 0
	raw, err := json.Marshal(state)
	if err != nil {
		return empty, false, err
	}
	if err := s.Redis.Set(ctx, pageKey, raw, 30*time.Minute).Err(); err != nil {
		return empty, false, err
	}
	result := responseFor(selected, state.Impression)
	result.HasMore = hasMore
	s.recordAsync(ctx, owner, selected, state.Impression, position, time.Now().UnixMilli())
	return result, hasMore, nil
}
