package delivery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"eigenflux_server/rpc/sort/discovery"

	"github.com/redis/go-redis/v9"
)

const searchTTL = 24 * time.Hour

// The ranking is stored once. Random page tokens bind continuations to a fixed
// page size and prevent clients from inventing offsets into another search.
type searchSnapshot struct {
	Execution   discovery.Execution `json:"execution"`
	Hash        string              `json:"hash"`
	Impression  string              `json:"impression_id"`
	Limit       int                 `json:"limit"`
	RequestTime int64               `json:"request_time"`
	Tokens      []string            `json:"tokens"`
}

func searchHash(r discovery.Request) string {
	r.Cursor = ""
	raw, _ := json.Marshal(r)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func searchToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func searchKey(owner int64, session string) string {
	return fmt.Sprintf("discovery:search:%d:%s", owner, session)
}

func (s Service) freezeSearch(ctx context.Context, owner int64, r discovery.Request, x discovery.Execution, impression string, now int64) (searchSnapshot, string, error) {
	limit := r.Limit
	if limit == 0 {
		limit = 20
	}
	state := searchSnapshot{Execution: x, Hash: searchHash(r), Impression: impression, Limit: limit, RequestTime: now}
	if len(x.Candidates) <= limit {
		return state, "", nil
	}
	session := searchToken()
	for offset := 0; offset < len(x.Candidates); offset += limit {
		state.Tokens = append(state.Tokens, searchToken())
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return state, "", err
	}
	err = s.Redis.Set(ctx, searchKey(owner, session), raw, searchTTL).Err()
	return state, session, err
}

func (s Service) loadSearch(ctx context.Context, owner int64, r discovery.Request) (state searchSnapshot, session string, page int, err error) {
	parts := strings.Split(r.Cursor, ".")
	if len(parts) != 2 || len(parts[0]) != 32 || len(parts[1]) != 32 {
		return state, "", 0, discovery.Invalid("cursor", "invalid")
	}
	for _, part := range parts {
		if _, e := hex.DecodeString(part); e != nil {
			return state, "", 0, discovery.Invalid("cursor", "invalid")
		}
	}
	session = parts[0]
	raw, err := s.Redis.Get(ctx, searchKey(owner, session)).Result()
	if err == redis.Nil {
		return state, "", 0, discovery.Failure(410, "search_cursor_expired")
	}
	if err != nil {
		return state, "", 0, err
	}
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return state, "", 0, err
	}
	if state.Limit < 1 || state.Limit > 50 || len(state.Execution.Candidates) > discovery.MaxSnapshotCandidates {
		return state, "", 0, discovery.Failure(503, "invalid_search_snapshot")
	}
	if state.Hash != searchHash(r) {
		return state, "", 0, discovery.Failure(409, "search_cursor_request_mismatch")
	}
	for i := 1; i < len(state.Tokens); i++ {
		if state.Tokens[i] == parts[1] && i*state.Limit < len(state.Execution.Candidates) {
			return state, session, i, nil
		}
	}
	return state, "", 0, discovery.Invalid("cursor", "invalid")
}

func (state searchSnapshot) page(session string, page int) (discovery.Response, discovery.Execution, int) {
	position := page * state.Limit
	x := state.Execution
	x.Candidates = x.Candidates[position:min(position+state.Limit, len(x.Candidates))]
	response := responseFor(x, state.Impression)
	response.HasMore = position+len(x.Candidates) < len(state.Execution.Candidates)
	if response.HasMore {
		response.NextCursor = session + "." + state.Tokens[page+1]
	}
	return response, x, position
}
