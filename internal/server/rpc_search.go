package server

import (
	"context"

	"prism/internal/search"
)

func (s *Server) registerSearch() {
	a := s.App
	rpc(s, "search.all", func(ctx context.Context, r struct {
		Q string `json:"q"`
	}) ([]search.Result, error) {
		return a.Search.All(ctx, r.Q)
	})
}
