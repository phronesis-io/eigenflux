// Package commissiondiscovery exposes authenticated discovery Facade routes.
// Commission source-of-truth writes remain owned by the Commission API.
package commissiondiscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/kitex/client/callopt"
	"go.opentelemetry.io/otel/trace"

	"eigenflux_server/api/middleware"
	sortmodel "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pkg/logger"
)

const (
	attributionStream  = "stream:commission:discovery"
	defaultResultLimit = int32(20)
	maxResultLimit     = int32(100)
	maxQueryBytes      = 512
)

type SortClient interface {
	SearchCommissions(context.Context, *sortmodel.SearchCommissionsReq, ...callopt.Option) (*sortmodel.SearchCommissionsResp, error)
	RecommendCommissions(context.Context, *sortmodel.RecommendCommissionsReq, ...callopt.Option) (*sortmodel.RecommendCommissionsResp, error)
}

type IDGenerator interface {
	NextID() (int64, error)
}

type Publisher func(context.Context, string, map[string]interface{}) (string, error)

type Service struct {
	sortClient SortClient
	idgen      IDGenerator
	publish    Publisher
}

func New(sortClient SortClient, idgen IDGenerator, publish Publisher) *Service {
	return &Service{sortClient: sortClient, idgen: idgen, publish: publish}
}

func Register(h *server.Hertz, service *Service) {
	h.GET("/api/v1/commissions/search", middleware.AuthMiddleware(), service.Search)
	h.GET("/api/v1/commissions/recommendations", middleware.AuthMiddleware(), service.Recommend)
}

type candidateDTO struct {
	CommissionID string  `json:"commission_id"`
	Score        float64 `json:"score"`
	Features     *string `json:"features,omitempty"`
}

type filters struct {
	MinPriceFen           *int64 `json:"min_price_fen,omitempty"`
	MaxPriceFen           *int64 `json:"max_price_fen,omitempty"`
	MinPromisedDeliveryMS *int64 `json:"min_promised_delivery_ms,omitempty"`
	MaxPromisedDeliveryMS *int64 `json:"max_promised_delivery_ms,omitempty"`
}

func (f filters) thrift() *sortmodel.CommissionSearchFilters {
	if f.MinPriceFen == nil && f.MaxPriceFen == nil && f.MinPromisedDeliveryMS == nil && f.MaxPromisedDeliveryMS == nil {
		return nil
	}
	return &sortmodel.CommissionSearchFilters{
		MinPriceFen:           f.MinPriceFen,
		MaxPriceFen:           f.MaxPriceFen,
		MinPromisedDeliveryMs: f.MinPromisedDeliveryMS,
		MaxPromisedDeliveryMs: f.MaxPromisedDeliveryMS,
	}
}

func parseOptionalNonNegative(c *app.RequestContext, name string) (*int64, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return nil, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return &value, nil
}

func parseRequest(c *app.RequestContext) (filters, int32, error) {
	var parsed filters
	var err error
	if parsed.MinPriceFen, err = parseOptionalNonNegative(c, "min_price_fen"); err != nil {
		return parsed, 0, err
	}
	if parsed.MaxPriceFen, err = parseOptionalNonNegative(c, "max_price_fen"); err != nil {
		return parsed, 0, err
	}
	if parsed.MinPromisedDeliveryMS, err = parseOptionalNonNegative(c, "min_promised_delivery_ms"); err != nil {
		return parsed, 0, err
	}
	if parsed.MaxPromisedDeliveryMS, err = parseOptionalNonNegative(c, "max_promised_delivery_ms"); err != nil {
		return parsed, 0, err
	}
	if parsed.MinPriceFen != nil && parsed.MaxPriceFen != nil && *parsed.MinPriceFen > *parsed.MaxPriceFen {
		return parsed, 0, fmt.Errorf("min_price_fen cannot exceed max_price_fen")
	}
	if parsed.MinPromisedDeliveryMS != nil && parsed.MaxPromisedDeliveryMS != nil && *parsed.MinPromisedDeliveryMS > *parsed.MaxPromisedDeliveryMS {
		return parsed, 0, fmt.Errorf("min_promised_delivery_ms cannot exceed max_promised_delivery_ms")
	}
	limit := defaultResultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		value, parseErr := strconv.ParseInt(raw, 10, 32)
		if parseErr != nil || value < 1 || value > int64(maxResultLimit) {
			return parsed, 0, fmt.Errorf("limit must be between 1 and %d", maxResultLimit)
		}
		limit = int32(value)
	}
	return parsed, limit, nil
}

func callerAgentID(c *app.RequestContext) (int64, bool) {
	value, ok := c.Get("agent_id")
	if !ok {
		return 0, false
	}
	agentID, ok := value.(int64)
	return agentID, ok && agentID > 0
}

func respond(c *app.RequestContext, status, code int, message string, data any) {
	payload := map[string]any{"code": code, "msg": message}
	if data != nil {
		payload["data"] = data
	}
	c.JSON(status, payload)
}

func candidatesDTO(candidates []*sortmodel.CommissionCandidate) []candidateDTO {
	result := make([]candidateDTO, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil || candidate.CommissionId <= 0 {
			continue
		}
		result = append(result, candidateDTO{
			CommissionID: strconv.FormatInt(candidate.CommissionId, 10),
			Score:        candidate.Score,
			Features:     candidate.Features,
		})
	}
	return result
}

func rpcError(ctx context.Context, c *app.RequestContext, operation string, err error) {
	logger.Ctx(ctx).Error("commission discovery RPC failed", "operation", operation, "err", err)
	respond(c, http.StatusServiceUnavailable, 503, "commission discovery is temporarily unavailable", nil)
}

func validateRPCResponse(ctx context.Context, c *app.RequestContext, operation string, baseCode int32, baseMessage string) bool {
	if baseCode == 0 {
		return true
	}
	logger.Ctx(ctx).Warn("commission discovery RPC rejected request", "operation", operation, "code", baseCode, "msg", baseMessage)
	if baseCode >= 400 && baseCode < 500 {
		respond(c, int(baseCode), int(baseCode), baseMessage, nil)
		return false
	}
	respond(c, http.StatusBadGateway, 502, "commission discovery failed", nil)
	return false
}

func (s *Service) serve(ctx context.Context, c *app.RequestContext, operation, query string, parsed filters, candidates []*sortmodel.CommissionCandidate) {
	impressionID, err := s.idgen.NextID()
	if err != nil {
		logger.Ctx(ctx).Error("commission impression ID generation failed", "err", err)
		respond(c, http.StatusServiceUnavailable, 503, "commission discovery is temporarily unavailable", nil)
		return
	}
	agentID, ok := callerAgentID(c)
	if !ok {
		respond(c, http.StatusUnauthorized, 401, "invalid or expired token", nil)
		return
	}
	result := candidatesDTO(candidates)
	impressionText := strconv.FormatInt(impressionID, 10)
	respond(c, http.StatusOK, 0, "success", map[string]any{
		"impression_id": impressionText,
		"candidates":    result,
	})
	s.publishAttribution(ctx, operation, impressionText, agentID, query, parsed, result)
}

func (s *Service) publishAttribution(requestContext context.Context, operation, impressionID string, agentID int64, query string, parsed filters, candidates []candidateDTO) {
	if s.publish == nil {
		return
	}
	filterJSON, _ := json.Marshal(parsed)
	candidateJSON, _ := json.Marshal(candidates)
	traceID := ""
	if spanContext := trace.SpanFromContext(requestContext).SpanContext(); spanContext.HasTraceID() {
		traceID = spanContext.TraceID().String()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := s.publish(ctx, attributionStream, map[string]interface{}{
			"impression_id": impressionID,
			"agent_id":      strconv.FormatInt(agentID, 10),
			"operation":     operation,
			"query":         query,
			"filters":       string(filterJSON),
			"candidates":    string(candidateJSON),
			"served_at":     time.Now().UnixMilli(),
			"trace_id":      traceID,
		})
		if err != nil {
			logger.Default().Warn("commission discovery attribution publish failed", "impressionID", impressionID, "err", err)
		}
	}()
}

func (s *Service) Search(ctx context.Context, c *app.RequestContext) {
	agentID, ok := callerAgentID(c)
	if !ok {
		respond(c, http.StatusUnauthorized, 401, "invalid or expired token", nil)
		return
	}
	_ = agentID
	query := strings.TrimSpace(c.Query("query"))
	if query == "" || len(query) > maxQueryBytes {
		respond(c, http.StatusBadRequest, 400, "query is required and must not exceed 512 bytes", nil)
		return
	}
	parsed, limit, err := parseRequest(c)
	if err != nil {
		respond(c, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	response, err := s.sortClient.SearchCommissions(ctx, &sortmodel.SearchCommissionsReq{Query: query, Filters: parsed.thrift(), Limit: &limit})
	if err != nil || response == nil || response.BaseResp == nil {
		rpcError(ctx, c, "search", err)
		return
	}
	if !validateRPCResponse(ctx, c, "search", response.BaseResp.Code, response.BaseResp.Msg) {
		return
	}
	s.serve(ctx, c, "search", query, parsed, response.Candidates)
}

func (s *Service) Recommend(ctx context.Context, c *app.RequestContext) {
	agentID, ok := callerAgentID(c)
	if !ok {
		respond(c, http.StatusUnauthorized, 401, "invalid or expired token", nil)
		return
	}
	parsed, limit, err := parseRequest(c)
	if err != nil {
		respond(c, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	response, err := s.sortClient.RecommendCommissions(ctx, &sortmodel.RecommendCommissionsReq{AgentId: agentID, Filters: parsed.thrift(), Limit: &limit})
	if err != nil || response == nil || response.BaseResp == nil {
		rpcError(ctx, c, "recommend", err)
		return
	}
	if !validateRPCResponse(ctx, c, "recommend", response.BaseResp.Code, response.BaseResp.Msg) {
		return
	}
	s.serve(ctx, c, "recommend", "", parsed, response.Candidates)
}
