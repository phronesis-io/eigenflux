package commissionintegration

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pkg/commissionindex"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

var (
	ErrInvalidConfiguration  = errors.New("invalid commission diagnostics configuration")
	ErrInvalidArgument       = errors.New("invalid commission diagnostics argument")
	ErrDependencyUnavailable = errors.New("commission diagnostics dependency unavailable")
)

type ProjectionDiagnostic struct {
	StreamLastID string
	Pending      int64
	Lag          int64
	DLQMatches   int64
}

type ProjectionState interface {
	Diagnostic(context.Context, int64) (ProjectionDiagnostic, error)
	Ready(context.Context) error
}

type Source interface {
	GetIndexSnapshot(context.Context, int64) (commissionindex.CatalogueSnapshot, error)
	GetStatistics(context.Context, int64) (commissionindex.StatisticsSnapshot, error)
	Ready(context.Context) error
}

type Index interface {
	Get(context.Context, int64) (commissionindex.Document, bool, error)
	Ready(context.Context) (int, error)
}

type Embedding interface {
	Ready(context.Context) (string, int, error)
}

type Diagnostic struct {
	CommissionID        string `json:"commission_id"`
	ProjectionStatus    string `json:"projection_status"`
	StreamLastID        string `json:"stream_last_id"`
	Pending             int64  `json:"pending"`
	Lag                 int64  `json:"lag"`
	DLQMatches          int64  `json:"dlq_matches"`
	CatalogueStatus     string `json:"catalogue_status"`
	SourceStatus        string `json:"source_status"`
	CatalogueVersion    int64  `json:"catalogue_version"`
	StatisticsStatus    string `json:"statistics_status"`
	StatisticsVersion   int64  `json:"statistics_version"`
	ESStatus            string `json:"es_status"`
	ESFound             bool   `json:"es_found"`
	ESActive            bool   `json:"es_active"`
	ESCatalogueVersion  int64  `json:"es_catalogue_version"`
	ESStatisticsVersion int64  `json:"es_statistics_version"`
}

type Status struct {
	Mode                string   `json:"mode"`
	ConsumerReady       bool     `json:"consumer_ready"`
	SourceReady         bool     `json:"source_ready"`
	ESReady             bool     `json:"es_ready"`
	EmbeddingReady      bool     `json:"embedding_ready"`
	EmbeddingProvider   string   `json:"embedding_provider"`
	EmbeddingDimensions int      `json:"embedding_dimensions"`
	ESIndexDimensions   int      `json:"es_index_dimensions"`
	Capabilities        []string `json:"capabilities"`
}

type Service struct {
	projection ProjectionState
	source     Source
	index      Index
	embedding  Embedding
}

func NewService(projection ProjectionState, source Source, index Index, embedding Embedding) (*Service, error) {
	if projection == nil || source == nil || index == nil || embedding == nil {
		return nil, ErrInvalidConfiguration
	}
	return &Service{projection: projection, source: source, index: index, embedding: embedding}, nil
}

func (s *Service) Status(ctx context.Context) Status {
	status := Status{
		Mode:         "test",
		Capabilities: []string{"commission-projection-diagnostics"},
	}
	status.ConsumerReady = s.projection.Ready(ctx) == nil
	status.SourceReady = s.source.Ready(ctx) == nil
	esDimensions, esErr := s.index.Ready(ctx)
	provider, embeddingDimensions, embeddingErr := s.embedding.Ready(ctx)
	status.EmbeddingProvider = strings.TrimSpace(provider)
	status.EmbeddingDimensions = embeddingDimensions
	status.ESIndexDimensions = esDimensions
	matchingDimensions := esDimensions > 0 && embeddingDimensions > 0 && esDimensions == embeddingDimensions
	status.ESReady = esErr == nil && matchingDimensions
	status.EmbeddingReady = embeddingErr == nil && status.EmbeddingProvider != "" && matchingDimensions
	return status
}

func (s *Service) Diagnostic(ctx context.Context, commissionID int64) (Diagnostic, error) {
	if commissionID <= 0 {
		return Diagnostic{}, ErrInvalidArgument
	}
	result := Diagnostic{
		CommissionID:     strconv.FormatInt(commissionID, 10),
		ProjectionStatus: "unavailable",
		CatalogueStatus:  "unavailable",
		StatisticsStatus: "unavailable",
		ESStatus:         "unavailable",
	}
	var projection ProjectionDiagnostic
	var projectionOK bool
	var catalogue commissionindex.CatalogueSnapshot
	var catalogueOK bool
	var statistics commissionindex.StatisticsSnapshot
	var statisticsOK bool
	var document commissionindex.Document
	var documentFound, documentOK bool
	var probes sync.WaitGroup
	probes.Add(4)
	go func() {
		defer probes.Done()
		var err error
		projection, err = s.projection.Diagnostic(ctx, commissionID)
		projectionOK = err == nil
	}()
	go func() {
		defer probes.Done()
		var err error
		catalogue, err = s.source.GetIndexSnapshot(ctx, commissionID)
		catalogueOK = err == nil && catalogue.CommissionID == commissionID
	}()
	go func() {
		defer probes.Done()
		var err error
		statistics, err = s.source.GetStatistics(ctx, commissionID)
		statisticsOK = err == nil && statistics.CommissionID == commissionID
	}()
	go func() {
		defer probes.Done()
		var err error
		document, documentFound, err = s.index.Get(ctx, commissionID)
		documentOK = err == nil && (!documentFound || document.CommissionID == commissionID)
	}()
	probes.Wait()
	if projectionOK {
		result.ProjectionStatus = "ok"
		result.StreamLastID = projection.StreamLastID
		result.Pending = projection.Pending
		result.Lag = projection.Lag
		result.DLQMatches = projection.DLQMatches
	}
	if catalogueOK {
		result.CatalogueStatus = "ok"
		result.SourceStatus = catalogue.Status
		result.CatalogueVersion = catalogue.CatalogueVersion
	}
	if statisticsOK {
		result.StatisticsStatus = "ok"
		result.StatisticsVersion = statistics.StatisticsVersion
	}
	if documentOK {
		result.ESStatus = "ok"
		result.ESFound = documentFound
		if documentFound {
			result.ESActive = document.Active
			result.ESCatalogueVersion = document.CatalogueVersion
			result.ESStatisticsVersion = document.StatisticsVersion
		}
	}
	return result, nil
}

const maxDiagnosticDLQEntries int64 = 256
const maxRedisStreamIDLength = 41

var errProjectionStateUnavailable = errors.New("commission projection state unavailable")

type StreamInfo struct {
	LastID string
	Length int64
}

type GroupInfo struct {
	Name    string
	Pending int64
	Lag     int64
}

type StreamMessage struct {
	ID     string
	Values map[string]any
}

type streamInspector interface {
	Info(context.Context, string) (StreamInfo, error)
	Groups(context.Context, string) ([]GroupInfo, error)
	Range(context.Context, string, string, string, int64, bool) ([]StreamMessage, error)
}

type goRedisStreamInspector struct {
	client *redis.Client
}

func (i goRedisStreamInspector) Info(ctx context.Context, stream string) (StreamInfo, error) {
	result, err := i.client.XInfoStream(ctx, stream).Result()
	if err != nil || result == nil {
		return StreamInfo{}, errProjectionStateUnavailable
	}
	return StreamInfo{LastID: result.LastGeneratedID, Length: result.Length}, nil
}

func (i goRedisStreamInspector) Groups(ctx context.Context, stream string) ([]GroupInfo, error) {
	result, err := i.client.XInfoGroups(ctx, stream).Result()
	if err != nil {
		return nil, errProjectionStateUnavailable
	}
	groups := make([]GroupInfo, 0, len(result))
	for _, group := range result {
		groups = append(groups, GroupInfo{Name: group.Name, Pending: group.Pending, Lag: group.Lag})
	}
	return groups, nil
}

func (i goRedisStreamInspector) Range(ctx context.Context, stream, start, stop string, count int64, reverse bool) ([]StreamMessage, error) {
	var result []redis.XMessage
	var err error
	if reverse {
		result, err = i.client.XRevRangeN(ctx, stream, start, stop, count).Result()
	} else {
		result, err = i.client.XRangeN(ctx, stream, start, stop, count).Result()
	}
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, errProjectionStateUnavailable
	}
	messages := make([]StreamMessage, 0, len(result))
	for _, message := range result {
		messages = append(messages, StreamMessage{ID: message.ID, Values: message.Values})
	}
	return messages, nil
}

type RedisProjectionState struct {
	inspector        streamInspector
	stream           string
	group            string
	deadLetterStream string
}

func NewRedisProjectionState(client *redis.Client, stream, group, deadLetterStream string) (*RedisProjectionState, error) {
	if client == nil || strings.TrimSpace(stream) == "" || strings.TrimSpace(group) == "" || strings.TrimSpace(deadLetterStream) == "" {
		return nil, ErrInvalidConfiguration
	}
	return newRedisProjectionState(goRedisStreamInspector{client: client}, stream, group, deadLetterStream), nil
}

func newRedisProjectionState(inspector streamInspector, stream, group, deadLetterStream string) *RedisProjectionState {
	return &RedisProjectionState{inspector: inspector, stream: stream, group: group, deadLetterStream: deadLetterStream}
}

func (s *RedisProjectionState) Ready(ctx context.Context) error {
	_, _, err := s.state(ctx)
	return err
}

func (s *RedisProjectionState) Diagnostic(ctx context.Context, commissionID int64) (ProjectionDiagnostic, error) {
	if commissionID <= 0 {
		return ProjectionDiagnostic{}, ErrInvalidArgument
	}
	stream, group, err := s.state(ctx)
	if err != nil {
		return ProjectionDiagnostic{}, errProjectionStateUnavailable
	}
	diagnostic := ProjectionDiagnostic{StreamLastID: stream.LastID, Pending: group.Pending, Lag: group.Lag}
	deadLetters, err := s.inspector.Range(ctx, s.deadLetterStream, "+", "-", maxDiagnosticDLQEntries, true)
	if err != nil {
		return ProjectionDiagnostic{}, errProjectionStateUnavailable
	}
	wantedID := strconv.FormatInt(commissionID, 10)
	for _, deadLetter := range deadLetters {
		originalStream, streamOK := structuredString(deadLetter.Values["original_stream"])
		originalID, idOK := structuredString(deadLetter.Values["original_id"])
		if !streamOK || !idOK || originalStream != s.stream || !validRedisStreamID(originalID) {
			continue
		}
		messages, rangeErr := s.inspector.Range(ctx, s.stream, originalID, originalID, 1, false)
		if rangeErr != nil {
			return ProjectionDiagnostic{}, errProjectionStateUnavailable
		}
		if len(messages) != 1 || messages[0].ID != originalID {
			continue
		}
		if commissionProjectionEnvelopeMatches(messages[0].Values, wantedID) {
			diagnostic.DLQMatches++
		}
	}
	return diagnostic, nil
}

func (s *RedisProjectionState) state(ctx context.Context) (StreamInfo, GroupInfo, error) {
	if s == nil || s.inspector == nil {
		return StreamInfo{}, GroupInfo{}, errProjectionStateUnavailable
	}
	stream, err := s.inspector.Info(ctx, s.stream)
	if err != nil || !validRedisStreamID(stream.LastID) {
		return StreamInfo{}, GroupInfo{}, errProjectionStateUnavailable
	}
	groups, err := s.inspector.Groups(ctx, s.stream)
	if err != nil {
		return StreamInfo{}, GroupInfo{}, errProjectionStateUnavailable
	}
	for _, group := range groups {
		if group.Name == s.group && group.Pending >= 0 && group.Lag >= 0 {
			return stream, group, nil
		}
	}
	return StreamInfo{}, GroupInfo{}, errProjectionStateUnavailable
}

func commissionProjectionEnvelopeMatches(values map[string]any, wantedID string) bool {
	schemaVersion, schemaOK := structuredString(values["schema_version"])
	aggregateType, typeOK := structuredString(values["aggregate_type"])
	aggregateID, idOK := structuredString(values["aggregate_id"])
	topic, topicOK := structuredString(values["topic"])
	if !schemaOK || !typeOK || !idOK || !topicOK || schemaVersion != "1" || aggregateID != wantedID {
		return false
	}
	expectedAggregateType, supported := commissionindex.ExpectedAggregateType(topic)
	return supported && aggregateType == expectedAggregateType
}

func validRedisStreamID(value string) bool {
	if value == "" || len(value) > maxRedisStreamIDLength {
		return false
	}
	milliseconds, sequence, found := strings.Cut(value, "-")
	if !found || milliseconds == "" || sequence == "" || strings.Contains(sequence, "-") ||
		(len(milliseconds) > 1 && milliseconds[0] == '0') || (len(sequence) > 1 && sequence[0] == '0') {
		return false
	}
	_, millisecondsErr := strconv.ParseUint(milliseconds, 10, 64)
	_, sequenceErr := strconv.ParseUint(sequence, 10, 64)
	return millisecondsErr == nil && sequenceErr == nil
}

func structuredString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case int:
		return strconv.Itoa(typed), true
	case uint64:
		return strconv.FormatUint(typed, 10), true
	default:
		return "", false
	}
}

type EmbeddingClient interface {
	GetEmbedding(context.Context, string) ([]float32, error)
}

const (
	defaultEmbeddingReadinessTTL = 30 * time.Second
	defaultEmbeddingFailureTTL   = 2 * time.Second
)

type embeddingReadiness struct {
	provider   string
	dimensions int
	err        error
	expiresAt  time.Time
}

type EmbeddingProbe struct {
	provider   string
	dimensions int
	client     EmbeddingClient
	now        func() time.Time
	successTTL time.Duration
	failureTTL time.Duration
	mu         sync.Mutex
	cache      embeddingReadiness
	group      singleflight.Group
}

func NewEmbeddingProbe(provider string, dimensions int, client EmbeddingClient) *EmbeddingProbe {
	return newEmbeddingProbeWithClock(provider, dimensions, client, time.Now, defaultEmbeddingReadinessTTL, defaultEmbeddingFailureTTL)
}

func newEmbeddingProbeWithClock(provider string, dimensions int, client EmbeddingClient, now func() time.Time, successTTL, failureTTL time.Duration) *EmbeddingProbe {
	return &EmbeddingProbe{
		provider: strings.TrimSpace(provider), dimensions: dimensions, client: client,
		now: now, successTTL: successTTL, failureTTL: failureTTL,
	}
}

func (p *EmbeddingProbe) Ready(ctx context.Context) (string, int, error) {
	if p == nil || p.client == nil || p.provider == "" || p.dimensions <= 0 || p.now == nil || p.successTTL <= 0 || p.failureTTL <= 0 {
		return "", 0, ErrDependencyUnavailable
	}
	if cached, ok := p.cached(); ok {
		return cached.provider, cached.dimensions, cached.err
	}
	value, _, _ := p.group.Do("readiness", func() (any, error) {
		if cached, ok := p.cached(); ok {
			return cached, nil
		}
		result := embeddingReadiness{provider: p.provider, dimensions: p.dimensions}
		vector, err := p.client.GetEmbedding(ctx, "commission projection readiness")
		ttl := p.successTTL
		if err != nil || len(vector) != p.dimensions {
			result.err = ErrDependencyUnavailable
			ttl = p.failureTTL
		}
		result.expiresAt = p.now().Add(ttl)
		p.mu.Lock()
		p.cache = result
		p.mu.Unlock()
		return result, nil
	})
	result := value.(embeddingReadiness)
	return result.provider, result.dimensions, result.err
}

func (p *EmbeddingProbe) cached() (embeddingReadiness, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache.expiresAt.IsZero() || !p.now().Before(p.cache.expiresAt) {
		return embeddingReadiness{}, false
	}
	return p.cache, true
}
