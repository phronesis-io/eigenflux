package consumer

import (
	"context"
	"eigenflux_server/pkg/commissionindex"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/metrics"
	"fmt"
	"strconv"
	"strings"
)

const (
	commissionPublishedTopic = commissionindex.PublishedTopic
	commissionOfflineTopic   = commissionindex.OfflineTopic
	commissionStatsTopic     = commissionindex.StatisticsTopic
)

type CommissionEmbedder interface {
	GetEmbedding(context.Context, string) ([]float32, error)
}

type CommissionIndexConsumer struct {
	source   commissionindex.Source
	store    commissionindex.Store
	embedder CommissionEmbedder
	runtime  StreamConsumer
}

func NewCommissionIndexConsumer(cfg *config.Config, source commissionindex.Source, store commissionindex.Store, embedder CommissionEmbedder) *CommissionIndexConsumer {
	c := &CommissionIndexConsumer{source: source, store: store, embedder: embedder}
	c.runtime = StreamConsumer{Name: "CommissionIndexConsumer", Stream: cfg.CommissionStream, Group: cfg.CommissionConsumerGroup, ConsumerName: "commission-index", MetricsLabel: "commission:index", Workers: cfg.CommissionConsumerWorkers, MaxRetries: int64(cfg.CommissionConsumerRetries), DeadLetterStream: cfg.CommissionDeadLetterStream, UnbufferedDispatch: true, FatalOnGroupCreateError: false, Handle: c.Handle}
	return c
}

func (c *CommissionIndexConsumer) Start(ctx context.Context) { c.runtime.Run(ctx) }

func (c *CommissionIndexConsumer) Handle(ctx context.Context, _ string, values map[string]any) HandleResult {
	event, err := parseCommissionEvent(values)
	if err != nil {
		return HandleFailure
	}
	catalogue, err := c.source.GetIndexSnapshot(ctx, event.CommissionID)
	if err != nil {
		metrics.CommissionProjectionFailures.WithLabelValues("commission_source").Inc()
		return HandleRetry
	}
	statistics, err := c.source.GetStatistics(ctx, event.CommissionID)
	if err != nil {
		metrics.CommissionProjectionFailures.WithLabelValues("order_source").Inc()
		return HandleRetry
	}
	if catalogue.CommissionID != event.CommissionID || catalogue.CatalogueVersion <= 0 || statistics.StatisticsVersion < 0 {
		return HandleRetry
	}
	if !strings.EqualFold(catalogue.Status, "active") || (event.Topic == commissionOfflineTopic && catalogue.CatalogueVersion <= event.AggregateVersion) {
		if err := c.store.Upsert(ctx, commissionindex.Tombstone(catalogue, statistics)); err != nil {
			metrics.CommissionProjectionFailures.WithLabelValues("index_write").Inc()
			return HandleRetry
		}
		return HandleSuccess
	}
	if c.embedder == nil {
		metrics.CommissionProjectionFailures.WithLabelValues("embedding").Inc()
		return HandleRetry
	}
	embedding, err := c.embedder.GetEmbedding(ctx, commissionindex.EmbeddingInput(catalogue))
	if err != nil {
		metrics.CommissionProjectionFailures.WithLabelValues("embedding").Inc()
		return HandleRetry
	}
	if err := c.store.Upsert(ctx, commissionindex.BuildDocument(catalogue, statistics, embedding)); err != nil {
		metrics.CommissionProjectionFailures.WithLabelValues("index_write").Inc()
		return HandleRetry
	}
	return HandleSuccess
}

type commissionEvent struct {
	CommissionID     int64
	AggregateVersion int64
	Topic            string
}

func parseCommissionEvent(values map[string]any) (commissionEvent, error) {
	read := func(key string) (string, error) {
		value, ok := values[key]
		if !ok {
			return "", fmt.Errorf("missing %s", key)
		}
		switch v := value.(type) {
		case string:
			return v, nil
		case []byte:
			return string(v), nil
		default:
			return fmt.Sprint(v), nil
		}
	}
	for _, key := range []string{"event_id", "schema_version", "topic", "aggregate_type", "aggregate_id", "aggregate_version", "occurred_at", "payload_json"} {
		if _, err := read(key); err != nil {
			return commissionEvent{}, err
		}
	}
	schema, _ := read("schema_version")
	if schema != "1" {
		return commissionEvent{}, fmt.Errorf("unsupported schema version %q", schema)
	}
	topic, _ := read("topic")
	expectedAggregateType, supported := commissionindex.ExpectedAggregateType(topic)
	if !supported {
		return commissionEvent{}, fmt.Errorf("unsupported topic %q", topic)
	}
	aggregateType, _ := read("aggregate_type")
	if aggregateType != expectedAggregateType {
		return commissionEvent{}, fmt.Errorf("unexpected aggregate type %q", aggregateType)
	}
	idText, _ := read("aggregate_id")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		return commissionEvent{}, fmt.Errorf("invalid aggregate ID")
	}
	versionText, _ := read("aggregate_version")
	version, err := strconv.ParseInt(versionText, 10, 64)
	if err != nil || version <= 0 {
		return commissionEvent{}, fmt.Errorf("invalid aggregate version")
	}
	return commissionEvent{CommissionID: id, AggregateVersion: version, Topic: topic}, nil
}
