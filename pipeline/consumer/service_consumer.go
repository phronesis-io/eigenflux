package consumer

import (
	"bytes"
	"context"
	"fmt"
	"strconv"

	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
	tradedal "eigenflux_server/rpc/trade/dal"
)

const (
	serviceStream       = "stream:trade:service"
	serviceGroup        = "cg:trade:service"
	serviceConsumerName = "service-worker-1"
	serviceMetricsLabel = "trade:service"
	serviceMaxRetries   = 5
)

type ServiceConsumer struct {
	embeddingClient *embedding.Client
	llm             EnrichLLM
	runner          *StreamConsumer
}

func NewServiceConsumer(cfg *config.Config, prompts *llm.PromptRegistry) *ServiceConsumer {
	c := &ServiceConsumer{
		embeddingClient: embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions),
		llm:             &llmEnrichAdapter{c: llm.NewClient(cfg, prompts)},
	}
	c.runner = &StreamConsumer{
		Name:         "ServiceConsumer",
		Stream:       serviceStream,
		Group:        serviceGroup,
		ConsumerName: serviceConsumerName,
		MetricsLabel: serviceMetricsLabel,
		Workers:      2,
		MaxRetries:   serviceMaxRetries,
		Handle:       c.handle,
	}
	return c
}

func (c *ServiceConsumer) Start(ctx context.Context) { c.runner.Run(ctx) }

// llmEnrichAdapter satisfies EnrichLLM by delegating to pipeline/llm.Client.Call.
type llmEnrichAdapter struct{ c *llm.Client }

func (a *llmEnrichAdapter) Complete(ctx context.Context, prompt string) (string, error) {
	return a.c.Call(ctx, prompt, "service_enrichment")
}

func (c *ServiceConsumer) handle(ctx context.Context, msgID string, values map[string]any) HandleResult {
	serviceIDStr, ok := values["service_id"].(string)
	if !ok {
		logger.Default().Warn("ServiceConsumer invalid message: missing service_id")
		return HandleFailure
	}
	serviceID, err := strconv.ParseInt(serviceIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("ServiceConsumer invalid service_id", "serviceID", serviceIDStr)
		return HandleFailure
	}

	logger.Default().Info("ServiceConsumer processing service", "serviceID", serviceID)

	svc, err := tradedal.GetService(db.DB, serviceID)
	if err != nil {
		logger.Default().Warn("ServiceConsumer service not found", "serviceID", serviceID, "err", err)
		return HandleFailure
	}

	selfEmbeddingText := svc.Title + " " + svc.CapabilityDesc + " " + svc.CallSpecText
	selfEmb, err := c.embeddingClient.GetEmbedding(ctx, selfEmbeddingText)
	if err != nil {
		logger.Default().Error("ServiceConsumer self-embedding failed", "serviceID", serviceID, "err", err)
		return HandleRetry
	}

	enrichInput := EnrichInput{
		Title:          svc.Title,
		CapabilityDesc: svc.CapabilityDesc,
		CallSpecText:   svc.CallSpecText,
	}
	if svc.CallSpecSchema != nil {
		enrichInput.CallSpecSchema = *svc.CallSpecSchema
	}
	enriched, err := EnrichService(ctx, c.llm, enrichInput)
	if err != nil {
		logger.Default().Warn("ServiceConsumer enrich failed", "serviceID", serviceID, "err", err)
		return HandleRetry
	}

	var usageEmb []float32
	if u, err := c.embeddingClient.GetEmbedding(ctx, enriched.UseCases); err == nil {
		usageEmb = u
	} else {
		logger.Default().Warn("ServiceConsumer usage-embedding failed", "serviceID", serviceID, "err", err)
		// Continue without — doc is still searchable via self embedding + BM25 fields.
	}

	if err := tradedal.UpdateServiceEnrichment(db.DB,
		svc.ServiceID,
		enriched.CapabilityTags,
		enriched.UseCases,
		string(enriched.CanonicalInputs),
		string(enriched.CanonicalOutputs),
		enriched.EnrichmentVersion,
	); err != nil {
		logger.Default().Warn("ServiceConsumer persist enrichment failed", "serviceID", serviceID, "err", err)
		return HandleRetry
	}

	doc := buildServiceDoc(svc, enriched, selfEmb, usageEmb)
	body, err := json.Marshal(doc)
	if err != nil {
		logger.Default().Error("ServiceConsumer failed to marshal service doc", "serviceID", serviceID, "err", err)
		return HandleFailure
	}

	res, err := es.Client.Index(
		es.ServicesIndexName,
		bytes.NewReader(body),
		es.Client.Index.WithContext(ctx),
		es.Client.Index.WithDocumentID(fmt.Sprintf("%d", serviceID)),
	)
	if err != nil {
		logger.Default().Error("ServiceConsumer failed to index service to ES", "serviceID", serviceID, "err", err)
		return HandleRetry
	}
	defer res.Body.Close()
	if res.IsError() {
		logger.Default().Error("ServiceConsumer ES index error", "serviceID", serviceID, "status", res.String())
		return HandleRetry
	}

	logger.Default().Info("ServiceConsumer service indexed to ES with enrichment", "serviceID", serviceID, "tags", len(enriched.CapabilityTags), "hasUsageEmb", usageEmb != nil)
	return HandleSuccess
}

// buildServiceDoc assembles the ES document. usageEmb may be nil — in that
// case the field is omitted so the doc remains valid against the mapping.
// The status field mirrors the PG enum (draft/active/offline) and is the
// hard filter every SearchServices recall lane applies, so it must always
// be written or the doc will be invisible to search.
func buildServiceDoc(svc *tradedal.TradingService, en *EnrichOutput, selfEmb, usageEmb []float32) map[string]any {
	doc := map[string]any{
		"service_id":           svc.ServiceID,
		"seller_agent_id":      svc.SellerAgentID,
		"status":               serviceStatusString(svc.Status),
		"title":                svc.Title,
		"capability_desc":      svc.CapabilityDesc,
		"call_spec_text":       svc.CallSpecText,
		"amount_atomic":        svc.AmountAtomic,
		"asset":                svc.Asset,
		"delivery_deadline_ms": svc.DeliveryDeadlineMs,
		"embedding":            selfEmb,
		"capability_tags":      en.CapabilityTags,
		"use_cases":            en.UseCases,
		"updated_at":           svc.UpdatedAt,
	}
	if usageEmb != nil {
		doc["usage_embedding"] = usageEmb
	}
	return doc
}

func serviceStatusString(s int16) string {
	switch s {
	case tradedal.ServiceStatusActive:
		return "active"
	case tradedal.ServiceStatusOffline:
		return "offline"
	default:
		return "draft"
	}
}
