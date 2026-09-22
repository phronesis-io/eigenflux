package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

type Service struct {
	Engine *Engine
	Store  Store
}
type Operation struct {
	Name, Payload, IdempotencyKey string
	ResourceID                    int64
}

func (s *Service) Run(ctx context.Context, owner int64, r Operation, now int64) (any, error) {
	e := s.Engine
	switch r.Name {
	case "search", "recommendation", "legacy_search", "legacy_prefetch":
		in, err := Decode[Request]([]byte(r.Payload))
		if err != nil {
			return nil, err
		}
		mode := Mode(r.Name)
		if r.Name == "legacy_search" {
			in.LegacyLimit = true
			mode = Search
		}
		if r.Name == "legacy_prefetch" {
			in.LegacyPrefetch = 20
			mode = Recommendation
		}
		return e.Execute(ctx, owner, in, mode, now)
	case "need_create", "need_update":
		input, err := Decode[NeedInput]([]byte(r.Payload))
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(input)
		bodyHash := fmt.Sprintf("%x", sha256.Sum256(raw))
		if r.Name == "need_create" {
			prior, found, err := s.Store.Idempotent(ctx, owner, r.IdempotencyKey, bodyHash)
			if err != nil || found {
				return prior, err
			}
		}
		id := r.ResourceID
		if r.Name == "need_create" {
			if input.ExpectedRevision != 0 {
				return nil, Invalid("expected_revision", "create_must_omit")
			}
			id, err = e.IDs.NextID()
			if err != nil {
				return nil, err
			}
		} else {
			if _, err = s.Store.Get(ctx, owner, id, true); err != nil {
				return nil, err
			}
		}
		languages := []string{}
		if input.Defaults.Language == "card" {
			owner, err := e.Sources.Owner(ctx, owner)
			if err != nil {
				return nil, err
			}
			languages = owner.Languages
		}
		compiled, err := e.Compiler.Need(ctx, owner, id, now, input, languages, true)
		if err != nil {
			return nil, err
		}
		if r.Name == "need_create" {
			return s.Store.CreateWithHash(ctx, compiled, r.IdempotencyKey, bodyHash)
		}
		return s.Store.Update(ctx, compiled, input.ExpectedRevision)
	case "need_get":
		c, err := s.Store.Get(ctx, owner, r.ResourceID, true)
		if err == nil && c.State == "active" && !c.Active(now) {
			c.State = "expired"
		}
		return c, err
	case "need_list":
		in, err := Decode[struct {
			State  string `json:"state"`
			Cursor int64  `json:"cursor,string"`
			Limit  int    `json:"limit"`
		}]([]byte(r.Payload))
		if err != nil {
			return nil, err
		}
		if in.Limit == 0 {
			in.Limit = 20
		}
		if in.Limit < 1 || in.Limit > 100 || in.Cursor < 0 {
			return nil, Invalid("pagination", "invalid")
		}
		rows, err := s.Store.List(ctx, owner, in.State, in.Cursor, in.Limit+1, now)
		if err != nil {
			return nil, err
		}
		cursor := ""
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			cursor = fmt.Sprint(rows[len(rows)-1].ID)
		}
		return map[string]any{"needs": rows, "next_cursor": cursor}, nil
	case "need_state":
		in, err := Decode[struct {
			State    string `json:"state"`
			Revision int64  `json:"expected_revision"`
		}]([]byte(r.Payload))
		if err != nil {
			return nil, err
		}
		return s.Store.SetState(ctx, owner, r.ResourceID, in.Revision, in.State, now)
	case "taxonomy":
		in, err := Decode[struct {
			Query    string `json:"query"`
			Category string `json:"category"`
			Subtype  string `json:"subtype"`
			Limit    int    `json:"limit"`
		}]([]byte(r.Payload))
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Query) == "" || len(in.Query) > 8000 {
			return nil, Invalid("query", "invalid_length")
		}
		if in.Limit == 0 {
			in.Limit = 10
		}
		if in.Limit < 1 || in.Limit > 20 {
			return nil, Invalid("limit", "out_of_range")
		}
		if in.Category != "" && !e.Compiler.Taxonomy.ValidBranch(in.Category, in.Subtype) {
			return nil, Invalid("category", "invalid_parent")
		}
		v, err := e.Compiler.Embedder.GetEmbedding(ctx, in.Query)
		if err != nil {
			return nil, Failure(503, "embedding_unavailable")
		}
		return map[string]any{"matches": e.Compiler.Taxonomy.Search(in.Query, in.Category, in.Subtype, v, .8, in.Limit), "taxonomy_version": e.Compiler.Taxonomy.Version}, nil
	}
	return nil, Invalid("operation", "unsupported")
}
