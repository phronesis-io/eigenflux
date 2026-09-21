package discoveryserve

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"eigenflux_server/pkg/discovery"
	"github.com/redis/go-redis/v9"
)

type PageExecutor interface {
	RefreshPage(context.Context, int64, discovery.Execution, int64) (discovery.Execution, error)
}
type pageState struct {
	Impression string              `json:"impression_id"`
	Position   int                 `json:"position"`
	Execution  discovery.Execution `json:"execution"`
}
type frozenExecutor struct {
	Executor
	x discovery.Execution
}

func (e frozenExecutor) Execute(context.Context, int64, discovery.Request, discovery.Mode, int64) (discovery.Execution, error) {
	return e.x, nil
}

// ServePage is the compatibility-only single-item Feed cursor. Cursor advance,
// impression history and the delivered sample share one Redis transaction.
func (s Service) ServePage(ctx context.Context, owner int64, action string, prepare func(context.Context, *discovery.Execution) error) (discovery.Response, bool, error) {
	empty := discovery.Response{Mode: discovery.Recommendation, PipelineVersion: discovery.PipelineVersion, Status: "exhausted", Items: []discovery.ResultItem{}}
	if owner <= 0 {
		return empty, false, discovery.Failure(401, "unauthorized")
	}
	if action != "refresh" && action != "load_more" {
		return empty, false, discovery.Invalid("action", "invalid")
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
	locked, err := s.Redis.SetNX(ctx, lockKey, lockToken, 60*time.Second).Result()
	if err != nil {
		return empty, false, err
	}
	if !locked {
		return empty, false, discovery.Failure(409, "request_in_progress")
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer done()
		s.Redis.Eval(cleanup, `if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`, []string{lockKey}, lockToken)
	}()
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
	validator, ok := s.Executor.(PageExecutor)
	if !ok {
		return empty, false, discovery.Failure(503, "page_validator_unavailable")
	}
	state.Execution, err = validator.RefreshPage(ctx, owner, state.Execution, time.Now().UnixMilli())
	if err != nil {
		// The owner lock prevents another request from installing a fresh page here.
		if e, ok := err.(*discovery.Error); ok && (e.Code == 404 || e.Code == 409) {
			_ = s.Redis.Del(ctx, pageKey).Err()
		}
		return empty, false, err
	}
	selected := state.Execution
	if len(selected.Candidates) > 1 {
		selected.Candidates = selected.Candidates[:1]
	}
	if prepare != nil {
		if err = prepare(ctx, &selected); err != nil {
			return empty, false, err
		}
	}
	position := state.Position
	state.Position += len(selected.Candidates)
	state.Execution.Candidates = state.Execution.Candidates[len(selected.Candidates):]
	hasMore := len(state.Execution.Candidates) > 0
	raw, err := json.Marshal(state)
	if err != nil {
		return empty, false, err
	}
	serve := s
	serve.Executor = frozenExecutor{Executor: s.Executor, x: selected}
	result, err := serve.serve(ctx, owner, discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}}, discovery.Recommendation, fmt.Sprintf("feed:%s:%d", state.Impression, position), commitPage{Impression: state.Impression, Position: position, Key: pageKey, Value: string(raw), Lock: lockKey, Token: lockToken})
	return result, hasMore, err
}
