package featureindex

import (
	"context"
	searchindex "eigenflux_server/rpc/sort/discovery/index"

	"strings"
	"time"
)

const (
	CommissionPublishedTopic  = "commission.published.v1"
	CommissionOfflineTopic    = "commission.offline.v1"
	CommissionStatisticsTopic = "commission.statistics.changed.v1"
)

func CommissionAggregateType(topic string) (string, bool) {
	switch topic {
	case CommissionPublishedTopic, CommissionOfflineTopic:
		return "commission", true
	case CommissionStatisticsTopic:
		return "commission_statistics", true
	default:
		return "", false
	}
}

type CommissionCatalogueSnapshot struct {
	CommissionID          int64
	SellerAgentID         int64
	Status                string
	CatalogueVersion      int64
	Title                 string
	CapabilityDescription string
	RequestSpecText       string
	DeliverySpecText      string
	Tags                  []string
	PriceFen              int64
	Currency              string
	PromisedDeliveryMS    int64
	CreatedAt             int64
	UpdatedAt             int64
}

type CommissionStatisticsSnapshot struct {
	CommissionID       int64
	SellerAgentID      int64
	CompletedCount     int64
	RefundedCount      int64
	CompletionRateBPS  int32
	AverageRatingMilli int32
	HasRating          bool
	AverageDeliveryMS  int64
	StatisticsVersion  int64
}

// Source is the only synchronous dependency of the Commission projection.
// Implementations must return an error for transport and BaseResp failures.
type CommissionSource interface {
	GetIndexSnapshot(context.Context, int64) (CommissionCatalogueSnapshot, error)
	ListActiveIndexSnapshots(context.Context, int64, int) ([]CommissionCatalogueSnapshot, int64, error)
	GetStatistics(context.Context, int64) (CommissionStatisticsSnapshot, error)
	BatchGetStatistics(context.Context, []int64) ([]CommissionStatisticsSnapshot, error)
}

type CommissionDocument struct {
	RetrievalSlots        searchindex.Slots `json:"retrieval_slots,omitempty"`
	CommissionID          int64             `json:"commission_id"`
	SellerAgentID         int64             `json:"seller_agent_id,omitempty"`
	Active                bool              `json:"active"`
	CatalogueVersion      int64             `json:"catalogue_version"`
	StatisticsVersion     int64             `json:"statistics_version"`
	Title                 string            `json:"title,omitempty"`
	CapabilityDescription string            `json:"capability_description,omitempty"`
	RequestSpecText       string            `json:"request_spec_text,omitempty"`
	DeliverySpecText      string            `json:"delivery_spec_text,omitempty"`
	Tags                  []string          `json:"tags,omitempty"`
	PriceFen              int64             `json:"price_fen"`
	Currency              string            `json:"currency,omitempty"`
	PromisedDeliveryMS    int64             `json:"promised_delivery_ms"`
	CompletedCount        int64             `json:"completed_count,omitempty"`
	RefundedCount         int64             `json:"refunded_count,omitempty"`
	CompletionRateBPS     int32             `json:"completion_rate_bps,omitempty"`
	AverageRatingMilli    int32             `json:"average_rating_milli,omitempty"`
	HasRating             bool              `json:"has_rating"`
	AverageDeliveryMS     int64             `json:"average_delivery_ms,omitempty"`
	SearchText            string            `json:"search_text,omitempty"`
	Embedding             []float32         `json:"embedding,omitempty"`
	UpdatedAt             time.Time         `json:"updated_at"`
}

func BuildCommissionDocument(c CommissionCatalogueSnapshot, s CommissionStatisticsSnapshot, embedding []float32) CommissionDocument {
	tags := append([]string(nil), c.Tags...)
	parts := []string{c.Title, c.CapabilityDescription, c.RequestSpecText, c.DeliverySpecText, strings.Join(tags, " ")}
	return CommissionDocument{
		CommissionID: c.CommissionID, SellerAgentID: c.SellerAgentID,
		Active: strings.EqualFold(c.Status, "active"), CatalogueVersion: c.CatalogueVersion,
		StatisticsVersion: s.StatisticsVersion, Title: c.Title, CapabilityDescription: c.CapabilityDescription,
		RequestSpecText: c.RequestSpecText, DeliverySpecText: c.DeliverySpecText, Tags: tags,
		PriceFen: c.PriceFen, Currency: c.Currency, PromisedDeliveryMS: c.PromisedDeliveryMS,
		CompletedCount: s.CompletedCount, RefundedCount: s.RefundedCount, CompletionRateBPS: s.CompletionRateBPS,
		AverageRatingMilli: s.AverageRatingMilli, HasRating: s.HasRating, AverageDeliveryMS: s.AverageDeliveryMS,
		SearchText: NormalizeCommissionText(parts...), Embedding: embedding, UpdatedAt: time.UnixMilli(c.UpdatedAt),
	}
}

func CommissionTombstone(c CommissionCatalogueSnapshot, s CommissionStatisticsSnapshot) CommissionDocument {
	d := BuildCommissionDocument(c, s, nil)
	d.Active = false
	return d
}

func CommissionEmbeddingInput(c CommissionCatalogueSnapshot) string {
	return NormalizeCommissionText(c.Title, c.CapabilityDescription, c.RequestSpecText, c.DeliverySpecText, strings.Join(c.Tags, " "))
}

func NormalizeCommissionText(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.Join(strings.Fields(part), " "); v != "" {
			clean = append(clean, v)
		}
	}
	return strings.Join(clean, " ")
}

type CommissionSearchStore interface {
	Upsert(context.Context, CommissionDocument) error
	UpsertStatistics(context.Context, CommissionStatisticsSnapshot) error
	Search(context.Context, CommissionSearchRequest) ([]CommissionHit, error)
}

type CommissionSearchRequest struct {
	Query                                                  string
	CommissionID                                           int64
	Embedding                                              []float32
	MinPriceFen, MaxPriceFen, MinDurationMS, MaxDurationMS int64
	Limit                                                  int
}
type CommissionHit struct {
	Document                    CommissionDocument
	KeywordScore, SemanticScore float64
}
