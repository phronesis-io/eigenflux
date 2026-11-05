package consolev2

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/featureindex"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

const marketSourcePageSize = 100

type marketServiceProvider struct {
	AgentID       string `json:"agent_id"`
	DisplayName   string `json:"display_name"`
	DisplayNameEn string `json:"display_name_en,omitempty"`
	ShortID       string `json:"short_id"`
	Model         string `json:"model"`
	IsFriend      bool   `json:"is_friend"`
}

type marketServiceItem struct {
	CommissionID          string                `json:"commission_id"`
	Title                 string                `json:"title"`
	CapabilityDescription string                `json:"capability_description"`
	PriceFen              int64                 `json:"price_fen"`
	Currency              string                `json:"currency"`
	CompletedCount        int64                 `json:"completed_count"`
	PromisedDeliveryMS    int64                 `json:"promised_delivery_ms"`
	AverageDeliveryMS     int64                 `json:"average_delivery_ms"`
	AverageRatingMilli    int32                 `json:"average_rating_milli"`
	HasRating             bool                  `json:"has_rating"`
	Provider              marketServiceProvider `json:"provider"`
}

type marketServicesResponse struct {
	Items      []marketServiceItem `json:"items"`
	Models     []string            `json:"models"`
	Total      int                 `json:"total"`
	NextCursor string              `json:"next_cursor"`
}

type marketServicesQuery struct {
	Text, Model         string
	FriendsOnly         bool
	HasCompleted        bool
	MaxDeliveryMS       *int64
	MinRatingMilli      *int64
	Limit               int
	AfterCount, AfterID int64
	HasCursor           bool
}

func parseMarketServicesQuery(c *app.RequestContext) (marketServicesQuery, error) {
	q := marketServicesQuery{Text: strings.TrimSpace(c.Query("q")), Model: strings.TrimSpace(c.Query("model")), Limit: 20}
	if len(q.Text) > 512 || len(q.Model) > 128 {
		return q, errors.New("query or model is too long")
	}
	for name, dest := range map[string]*bool{"friends_only": &q.FriendsOnly, "has_completed": &q.HasCompleted} {
		switch c.Query(name) {
		case "", "false":
		case "true":
			*dest = true
		default:
			return q, fmt.Errorf("%s must be true or false", name)
		}
	}
	for name, dest := range map[string]**int64{"max_delivery_ms": &q.MaxDeliveryMS, "min_rating_milli": &q.MinRatingMilli} {
		if raw := c.Query(name); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value < 0 || (name == "min_rating_milli" && value > 5000) {
				return q, fmt.Errorf("invalid %s", name)
			}
			*dest = &value
		}
	}
	if raw := c.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			return q, errors.New("limit must be between 1 and 100")
		}
		q.Limit = value
	}
	if raw := c.Query("cursor"); raw != "" {
		if len(raw) > 100 {
			return q, errors.New("invalid cursor")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		parts := strings.Split(string(decoded), ":")
		if err != nil || len(parts) != 2 {
			return q, errors.New("invalid cursor")
		}
		q.AfterCount, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || q.AfterCount < 0 {
			return q, errors.New("invalid cursor")
		}
		q.AfterID, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || q.AfterID <= 0 {
			return q, errors.New("invalid cursor")
		}
		q.HasCursor = true
	}
	return q, nil
}

// RegisterMarketServices uses the same browser identity and Commission access
// boundary as the homepage showcase, without its six-item/candidate limits.
func (s *Service) RegisterMarketServices(h *server.Hertz, source featureindex.CommissionSource, access app.HandlerFunc) {
	h.GET("/api/v2/console/market/services", s.ConsoleBFFHandlers(false, access, s.marketServicesHandler(source))...)
}

func (s *Service) marketServicesHandler(source featureindex.CommissionSource) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		q, err := parseMarketServicesQuery(c)
		if err != nil {
			fail(c, http.StatusBadRequest, "MARKET_QUERY_INVALID", err.Error(), nil)
			return
		}
		viewer, ok := agentID(c)
		if !ok {
			fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
			return
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		items, err := s.loadMarketServices(ctx, source, viewer)
		if err != nil {
			fail(c, http.StatusServiceUnavailable, "MARKET_SERVICES_UNAVAILABLE", "service catalogue temporarily unavailable", nil)
			return
		}
		reply(c, http.StatusOK, selectMarketServices(items, q))
	}
}

func (s *Service) loadMarketServices(ctx context.Context, source featureindex.CommissionSource, viewer int64) ([]marketServiceItem, error) {
	if source == nil {
		return nil, errors.New("Commission source unavailable")
	}
	result := make([]marketServiceItem, 0)
	seen := map[int64]bool{}
	seenCursors := map[int64]bool{0: true}
	cursor := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Read every source page. Filtering a bounded homepage sample would hide
		// matching services and produce incorrect model choices and totals.
		rows, next, err := source.ListActiveIndexSnapshots(ctx, cursor, marketSourcePageSize)
		if err != nil {
			return nil, err
		}
		if len(rows) > marketSourcePageSize || next < 0 || (next != 0 && (seenCursors[next] || len(rows) == 0)) {
			return nil, errors.New("invalid catalogue pagination")
		}
		ids := make([]int64, 0, len(rows))
		providerIDs := make([]int64, 0, len(rows))
		for _, row := range rows {
			if row.Status != "active" || row.CommissionID <= 0 || row.SellerAgentID <= 0 || row.Title == "" || seen[row.CommissionID] {
				return nil, errors.New("invalid published catalogue")
			}
			seen[row.CommissionID] = true
			ids, providerIDs = append(ids, row.CommissionID), append(providerIDs, row.SellerAgentID)
		}
		statistics, err := source.BatchGetStatistics(ctx, ids)
		if err != nil {
			return nil, err
		}
		byID := make(map[int64]featureindex.CommissionStatisticsSnapshot, len(statistics))
		for _, stats := range statistics {
			byID[stats.CommissionID] = stats
		}
		providers, err := s.marketProviders(ctx, providerIDs, viewer)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			stats, ok := byID[row.CommissionID]
			if !ok || stats.SellerAgentID != row.SellerAgentID || stats.CompletedCount < 0 || stats.AverageDeliveryMS < 0 || stats.AverageRatingMilli < 0 || stats.AverageRatingMilli > 5000 {
				return nil, errors.New("invalid Commission statistics")
			}
			provider, ok := providers[row.SellerAgentID]
			if !ok {
				continue
			}
			result = append(result, marketServiceItem{CommissionID: strconv.FormatInt(row.CommissionID, 10), Title: row.Title, CapabilityDescription: row.CapabilityDescription, PriceFen: row.PriceFen, Currency: row.Currency, CompletedCount: stats.CompletedCount, PromisedDeliveryMS: row.PromisedDeliveryMS, AverageDeliveryMS: stats.AverageDeliveryMS, AverageRatingMilli: stats.AverageRatingMilli, HasRating: stats.HasRating, Provider: provider})
		}
		if next == 0 {
			return result, nil
		}
		seenCursors[next], cursor = true, next
	}
}

func (s *Service) marketProviders(ctx context.Context, ids []int64, viewer int64) (map[int64]marketServiceProvider, error) {
	result := make(map[int64]marketServiceProvider)
	if len(ids) == 0 {
		return result, nil
	}
	var rows []struct {
		AgentID                int64
		AgentName, AgentNameEn string
		ShortID, Model         string
		IsFriend               bool
	}
	if err := s.db.WithContext(ctx).Table("agents a").Select(`a.agent_id, a.agent_name, COALESCE(a.agent_name_en,'') AS agent_name_en,
		COALESCE(a.short_id,'') AS short_id, COALESCE(settings.model,'') AS model,
		EXISTS (SELECT 1 FROM user_relations ur WHERE ur.from_uid=? AND ur.to_uid=a.agent_id AND ur.rel_type=1) AS is_friend`, viewer).
		Joins("LEFT JOIN agent_settings settings ON settings.agent_id=a.agent_id").Where("a.agent_id IN ? AND a.identity_state = ?", ids, "active").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.AgentID] = marketServiceProvider{AgentID: strconv.FormatInt(row.AgentID, 10), DisplayName: row.AgentName, DisplayNameEn: row.AgentNameEn, ShortID: row.ShortID, Model: strings.TrimSpace(row.Model), IsFriend: row.IsFriend}
	}
	return result, nil
}

func selectMarketServices(items []marketServiceItem, q marketServicesQuery) marketServicesResponse {
	result := marketServicesResponse{Items: make([]marketServiceItem, 0), Models: make([]string, 0)}
	models := map[string]bool{}
	filtered := make([]marketServiceItem, 0)
	text := strings.ToLower(q.Text)
	for _, item := range items {
		if model := item.Provider.Model; model != "" && !models[model] {
			models[model] = true
			result.Models = append(result.Models, model)
		}
		if (q.Model != "" && item.Provider.Model != q.Model) || (q.FriendsOnly && !item.Provider.IsFriend) || (q.HasCompleted && item.CompletedCount <= 0) {
			continue
		}
		if q.MaxDeliveryMS != nil && (item.CompletedCount <= 0 || item.AverageDeliveryMS <= 0 || item.AverageDeliveryMS > *q.MaxDeliveryMS) {
			continue
		}
		if q.MinRatingMilli != nil && (!item.HasRating || int64(item.AverageRatingMilli) < *q.MinRatingMilli) {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(strings.Join([]string{item.Title, item.CapabilityDescription, item.Provider.DisplayName, item.Provider.DisplayNameEn}, " ")), text) {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.Strings(result.Models)
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].CompletedCount != filtered[j].CompletedCount {
			return filtered[i].CompletedCount > filtered[j].CompletedCount
		}
		iID, _ := strconv.ParseInt(filtered[i].CommissionID, 10, 64)
		jID, _ := strconv.ParseInt(filtered[j].CommissionID, 10, 64)
		return iID > jID
	})
	result.Total = len(filtered)
	for _, item := range filtered {
		id, _ := strconv.ParseInt(item.CommissionID, 10, 64)
		if q.HasCursor && (item.CompletedCount > q.AfterCount || (item.CompletedCount == q.AfterCount && id >= q.AfterID)) {
			continue
		}
		if len(result.Items) == q.Limit {
			last := result.Items[len(result.Items)-1]
			result.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(last.CompletedCount, 10) + ":" + last.CommissionID))
			break
		}
		result.Items = append(result.Items, item)
	}
	return result
}
