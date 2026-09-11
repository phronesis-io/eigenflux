package consolev2

import (
	"github.com/cloudwego/hertz/pkg/app/server"

	"eigenflux_server/api/commissiondiscovery"
)

// RegisterCommissionDiscovery reuses the discovery facade with Agent V2 auth.
// A nil facade keeps the discovery feature flag's disabled surface unchanged.
func (s *Service) RegisterCommissionDiscovery(h *server.Hertz, discovery *commissiondiscovery.Service) {
	if discovery == nil {
		return
	}
	h.GET("/api/v2/commissions/search", s.agentAuth("feed:read"), s.requireCompleted, discovery.Search)
	h.GET("/api/v2/commissions/recommendations", s.agentAuth("feed:read"), s.requireCompleted, discovery.Recommend)
}
