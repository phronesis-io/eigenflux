package consolev2

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"eigenflux_server/pkg/featureindex"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/lib/pq"
	"golang.org/x/sync/singleflight"
)

const homeServiceCandidateLimit = 100
const homeServiceResultLimit = 6
const homeServiceCacheTTL = 30 * time.Second

type homeServiceProvider struct {
	AgentID     string `json:"agent_id"`
	DisplayName string `json:"display_name"`
	ShortID     string `json:"short_id"`
}
type homeServiceItem struct {
	CommissionID          string              `json:"commission_id"`
	Title                 string              `json:"title"`
	CapabilityDescription string              `json:"capability_description"`
	PriceFen              int64               `json:"price_fen"`
	Currency              string              `json:"currency"`
	CompletedCount        int64               `json:"completed_count"`
	Provider              homeServiceProvider `json:"provider"`
}
type homeServicesResponse struct {
	Items          []homeServiceItem `json:"items"`
	CandidateLimit int               `json:"candidate_limit"`
}
type rankedHomeService struct {
	Snapshot       featureindex.CommissionCatalogueSnapshot
	CompletedCount int64
}

// RegisterHomeServices exposes only published catalogue content, behind the
// same completed Console session and Commission allowlist as the trade BFF.
func (s *Service) RegisterHomeServices(h *server.Hertz, source featureindex.CommissionSource, access app.HandlerFunc) {
	var mu sync.Mutex
	var ranked []rankedHomeService
	var expires time.Time
	var refresh singleflight.Group
	load := func(ctx context.Context) ([]rankedHomeService, error) {
		mu.Lock()
		cached, valid := ranked, time.Now().Before(expires)
		mu.Unlock()
		if valid {
			return cached, nil
		}
		value, err, _ := refresh.Do("catalogue", func() (interface{}, error) {
			mu.Lock()
			cached, valid := ranked, time.Now().Before(expires)
			mu.Unlock()
			if valid {
				return cached, nil
			}
			refreshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := rankHomeServices(refreshCtx, source)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			ranked, expires = result, time.Now().Add(homeServiceCacheTTL)
			mu.Unlock()
			return result, nil
		})
		if err != nil {
			return nil, err
		}
		return value.([]rankedHomeService), nil
	}
	handler := func(ctx context.Context, c *app.RequestContext) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		candidates, err := load(ctx)
		if err != nil {
			fail(c, 503, "HOME_SERVICES_UNAVAILABLE", "service catalogue temporarily unavailable", nil)
			return
		}
		// Re-read current published snapshots even on a ranking cache hit. Offline
		// services and superseded public revisions must not remain on the homepage.
		current, err := currentHomeServices(ctx, source, candidates)
		if err != nil {
			fail(c, 503, "HOME_SERVICES_UNAVAILABLE", "service catalogue temporarily unavailable", nil)
			return
		}
		ids := make([]int64, 0, len(current))
		for _, item := range current {
			ids = append(ids, item.Snapshot.SellerAgentID)
		}
		var providers []struct {
			AgentID   int64
			AgentName string
			ShortID   string
		}
		if len(ids) > 0 {
			if err := s.db.WithContext(ctx).Raw(`SELECT agent_id, agent_name, COALESCE(short_id,'') AS short_id FROM agents WHERE agent_id = ANY(?) AND identity_state = 'active'`, pq.Array(ids)).Scan(&providers).Error; err != nil {
				fail(c, 503, "HOME_SERVICES_UNAVAILABLE", "service providers temporarily unavailable", nil)
				return
			}
		}
		byID := make(map[int64]homeServiceProvider, len(providers))
		for _, p := range providers {
			byID[p.AgentID] = homeServiceProvider{AgentID: strconv.FormatInt(p.AgentID, 10), DisplayName: p.AgentName, ShortID: p.ShortID}
		}
		result := homeServicesResponse{Items: make([]homeServiceItem, 0, len(current)), CandidateLimit: homeServiceCandidateLimit}
		for _, row := range current {
			item := row.Snapshot
			provider, ok := byID[item.SellerAgentID]
			if !ok {
				continue
			}
			result.Items = append(result.Items, homeServiceItem{CommissionID: strconv.FormatInt(item.CommissionID, 10), Title: item.Title, CapabilityDescription: item.CapabilityDescription, PriceFen: item.PriceFen, Currency: item.Currency, CompletedCount: row.CompletedCount, Provider: provider})
		}
		reply(c, http.StatusOK, result)
	}
	h.GET("/api/v2/console/home/services", s.ConsoleBFFHandlers(false, access, handler)...)
}

func rankHomeServices(ctx context.Context, source featureindex.CommissionSource) ([]rankedHomeService, error) {
	if source == nil {
		return nil, errors.New("Commission source unavailable")
	}
	snapshots, _, err := source.ListActiveIndexSnapshots(ctx, 0, homeServiceCandidateLimit)
	if err != nil {
		return nil, err
	}
	if len(snapshots) > homeServiceCandidateLimit {
		snapshots = snapshots[:homeServiceCandidateLimit]
	}
	ids := make([]int64, 0, len(snapshots))
	seen := map[int64]bool{}
	for _, item := range snapshots {
		if item.Status != "active" || item.CommissionID <= 0 || item.SellerAgentID <= 0 || item.Title == "" || seen[item.CommissionID] {
			return nil, errors.New("invalid published catalogue")
		}
		seen[item.CommissionID] = true
		ids = append(ids, item.CommissionID)
	}
	statistics, err := source.BatchGetStatistics(ctx, ids)
	if err != nil {
		return nil, err
	}
	counts := map[int64]featureindex.CommissionStatisticsSnapshot{}
	for _, item := range statistics {
		counts[item.CommissionID] = item
	}
	ranked := make([]rankedHomeService, 0, len(snapshots))
	for _, item := range snapshots {
		stats, ok := counts[item.CommissionID]
		if !ok || stats.SellerAgentID != item.SellerAgentID || stats.CompletedCount < 0 {
			return nil, errors.New("incomplete Commission statistics")
		}
		ranked = append(ranked, rankedHomeService{Snapshot: item, CompletedCount: stats.CompletedCount})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].CompletedCount != ranked[j].CompletedCount {
			return ranked[i].CompletedCount > ranked[j].CompletedCount
		}
		return ranked[i].Snapshot.CommissionID > ranked[j].Snapshot.CommissionID
	})
	return ranked, nil
}

func currentHomeServices(ctx context.Context, source featureindex.CommissionSource, candidates []rankedHomeService) ([]rankedHomeService, error) {
	result := make([]rankedHomeService, 0, homeServiceResultLimit)
	for _, candidate := range candidates {
		item, err := source.GetIndexSnapshot(ctx, candidate.Snapshot.CommissionID)
		if err != nil {
			return nil, err
		}
		if item.Status != "active" {
			continue
		}
		if item.CommissionID != candidate.Snapshot.CommissionID || item.SellerAgentID != candidate.Snapshot.SellerAgentID || item.Title == "" {
			return nil, errors.New("invalid current catalogue")
		}
		result = append(result, rankedHomeService{Snapshot: item, CompletedCount: candidate.CompletedCount})
		if len(result) == homeServiceResultLimit {
			break
		}
	}
	return result, nil
}
