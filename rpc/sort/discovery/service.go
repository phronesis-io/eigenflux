package discovery

import (
	"context"
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

	}
	return nil, Invalid("operation", "unsupported")
}
