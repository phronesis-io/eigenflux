package discovery

import (
	"context"
	"strings"
)

type Service struct {
	Engine *Engine
}
type Operation struct{ Name, Payload string }

func (s *Service) Run(ctx context.Context, owner int64, r Operation, now int64) (any, error) {
	e := s.Engine
	switch r.Name {
	case "search", "recommendation", "legacy_search", "legacy_prefetch", "search_prefetch":
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
			in.Prefetch = true
			mode = Recommendation
		}
		if r.Name == "search_prefetch" {
			in.Prefetch = true
			mode = Search
		}
		return e.Execute(ctx, owner, in, mode, now)
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
