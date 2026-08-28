package commissionintegration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"eigenflux_server/pkg/commissionindex"
)

type projectionFake struct {
	state ProjectionDiagnostic
	err   error
	ready error
}

func (f projectionFake) Diagnostic(context.Context, int64) (ProjectionDiagnostic, error) {
	return f.state, f.err
}
func (f projectionFake) Ready(context.Context) error { return f.ready }

type deadlineProjection struct{}

func (deadlineProjection) Diagnostic(ctx context.Context, _ int64) (ProjectionDiagnostic, error) {
	<-ctx.Done()
	return ProjectionDiagnostic{}, ctx.Err()
}
func (deadlineProjection) Ready(context.Context) error { return nil }

type contextAwareSource struct{ sourceFake }

func (f contextAwareSource) GetIndexSnapshot(ctx context.Context, id int64) (commissionindex.CatalogueSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return commissionindex.CatalogueSnapshot{}, err
	}
	return f.sourceFake.GetIndexSnapshot(ctx, id)
}
func (f contextAwareSource) GetStatistics(ctx context.Context, id int64) (commissionindex.StatisticsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return commissionindex.StatisticsSnapshot{}, err
	}
	return f.sourceFake.GetStatistics(ctx, id)
}

type contextAwareIndex struct{ indexFake }

func (f contextAwareIndex) Get(ctx context.Context, id int64) (commissionindex.Document, bool, error) {
	if err := ctx.Err(); err != nil {
		return commissionindex.Document{}, false, err
	}
	return f.indexFake.Get(ctx, id)
}

type sourceFake struct {
	catalogue     commissionindex.CatalogueSnapshot
	statistics    commissionindex.StatisticsSnapshot
	catalogueErr  error
	statisticsErr error
	ready         error
}

func (f sourceFake) GetIndexSnapshot(context.Context, int64) (commissionindex.CatalogueSnapshot, error) {
	return f.catalogue, f.catalogueErr
}
func (f sourceFake) GetStatistics(context.Context, int64) (commissionindex.StatisticsSnapshot, error) {
	return f.statistics, f.statisticsErr
}
func (f sourceFake) Ready(context.Context) error { return f.ready }

type indexFake struct {
	document   commissionindex.Document
	found      bool
	err        error
	dimensions int
	ready      error
}

func (f indexFake) Get(context.Context, int64) (commissionindex.Document, bool, error) {
	return f.document, f.found, f.err
}
func (f indexFake) Ready(context.Context) (int, error) { return f.dimensions, f.ready }

type embeddingFake struct {
	provider   string
	dimensions int
	err        error
}

func (f embeddingFake) Ready(context.Context) (string, int, error) {
	return f.provider, f.dimensions, f.err
}

func readyService(t *testing.T) *Service {
	t.Helper()
	service, err := NewService(
		projectionFake{state: ProjectionDiagnostic{StreamLastID: "123-0", Pending: 2, Lag: 3, DLQMatches: 4}},
		sourceFake{
			catalogue:  commissionindex.CatalogueSnapshot{CommissionID: 9223372036854775000, Status: "active", CatalogueVersion: 11, Title: "private source catalogue"},
			statistics: commissionindex.StatisticsSnapshot{CommissionID: 9223372036854775000, StatisticsVersion: 12},
		},
		indexFake{
			document: commissionindex.Document{CommissionID: 9223372036854775000, Active: true, CatalogueVersion: 10, StatisticsVersion: 9, SearchText: "private vector source", Embedding: []float32{1, 2}},
			found:    true, dimensions: 768,
		},
		embeddingFake{provider: "ollama", dimensions: 768},
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestStatusReportsFailClosedProjectionReadiness(t *testing.T) {
	status := readyService(t).Status(context.Background())
	if status.Mode != "test" || !status.ConsumerReady || !status.SourceReady || !status.ESReady || !status.EmbeddingReady ||
		status.EmbeddingProvider != "ollama" || status.EmbeddingDimensions != 768 || status.ESIndexDimensions != 768 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if len(status.Capabilities) != 1 || status.Capabilities[0] != "commission-projection-diagnostics" {
		t.Fatalf("capabilities=%v", status.Capabilities)
	}

	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"consumer_ready", "source_ready", "es_ready", "embedding_ready", "embedding_provider", "embedding_dimensions", "es_index_dimensions", "capabilities"} {
		if !strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("status JSON missing %q: %s", field, encoded)
		}
	}
}

func TestStatusNeverSilentlyReportsUnavailableDependencyReady(t *testing.T) {
	dependencyError := errors.New("private upstream body")
	tests := []struct {
		name       string
		projection projectionFake
		source     sourceFake
		index      indexFake
		embedding  embeddingFake
	}{
		{"consumer", projectionFake{ready: dependencyError}, sourceFake{}, indexFake{dimensions: 768}, embeddingFake{provider: "ollama", dimensions: 768}},
		{"source", projectionFake{}, sourceFake{ready: dependencyError}, indexFake{dimensions: 768}, embeddingFake{provider: "ollama", dimensions: 768}},
		{"ES", projectionFake{}, sourceFake{}, indexFake{ready: dependencyError}, embeddingFake{provider: "ollama", dimensions: 768}},
		{"embedding", projectionFake{}, sourceFake{}, indexFake{dimensions: 768}, embeddingFake{provider: "ollama", dimensions: 768, err: dependencyError}},
		{"dimension mismatch", projectionFake{}, sourceFake{}, indexFake{dimensions: 1536}, embeddingFake{provider: "ollama", dimensions: 768}},
		{"missing provider", projectionFake{}, sourceFake{}, indexFake{dimensions: 768}, embeddingFake{dimensions: 768}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, err := NewService(tc.projection, tc.source, tc.index, tc.embedding)
			if err != nil {
				t.Fatal(err)
			}
			status := service.Status(context.Background())
			switch tc.name {
			case "consumer":
				if status.ConsumerReady {
					t.Fatal("consumer reported ready")
				}
			case "source":
				if status.SourceReady {
					t.Fatal("source reported ready")
				}
			case "ES":
				if status.ESReady {
					t.Fatal("ES reported ready")
				}
			case "embedding":
				if status.EmbeddingReady {
					t.Fatal("embedding reported ready")
				}
			case "dimension mismatch":
				if status.ESReady || status.EmbeddingReady {
					t.Fatal("dimension mismatch reported ready")
				}
			case "missing provider":
				if status.EmbeddingReady {
					t.Fatal("missing provider reported ready")
				}
			}
		})
	}
}

func TestDiagnosticReturnsMetadataOnlyAndPreservesInt64ID(t *testing.T) {
	const id int64 = 9223372036854775000
	got, err := readyService(t).Diagnostic(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	want := Diagnostic{
		CommissionID: "9223372036854775000", ProjectionStatus: "ok", StreamLastID: "123-0", Pending: 2, Lag: 3, DLQMatches: 4,
		CatalogueStatus: "ok", SourceStatus: "active", CatalogueVersion: 11,
		StatisticsStatus: "ok", StatisticsVersion: 12,
		ESStatus: "ok", ESFound: true, ESActive: true, ESCatalogueVersion: 10, ESStatisticsVersion: 9,
	}
	if got != want {
		t.Fatalf("diagnostic=%#v, want %#v", got, want)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private source catalogue", "private vector source", "embedding", "payload", "token"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(secret)) {
			t.Fatalf("diagnostic leaked %q: %s", secret, encoded)
		}
	}
}

func TestDiagnosticReturnsOfflineSourceAndESVersions(t *testing.T) {
	service, err := NewService(projectionFake{}, sourceFake{
		catalogue:  commissionindex.CatalogueSnapshot{CommissionID: 42, Status: "offline", CatalogueVersion: 13},
		statistics: commissionindex.StatisticsSnapshot{CommissionID: 42, StatisticsVersion: 3},
	}, indexFake{
		document: commissionindex.Document{CommissionID: 42, Active: false, CatalogueVersion: 12, StatisticsVersion: 2},
		found:    true, dimensions: 768,
	}, embeddingFake{provider: "ollama", dimensions: 768})
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Diagnostic(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceStatus != "offline" || got.CatalogueVersion != 13 || got.StatisticsVersion != 3 ||
		!got.ESFound || got.ESActive || got.ESCatalogueVersion != 12 || got.ESStatisticsVersion != 2 {
		t.Fatalf("unexpected offline diagnostic: %#v", got)
	}
}

func TestDiagnosticRejectsInvalidIDAndPreservesDependencyStage(t *testing.T) {
	service := readyService(t)
	if _, err := service.Diagnostic(context.Background(), 0); err != ErrInvalidArgument {
		t.Fatalf("invalid ID error=%v", err)
	}
	const id int64 = 42
	readyProjection := projectionFake{state: ProjectionDiagnostic{StreamLastID: "1-0"}}
	readySource := sourceFake{
		catalogue:  commissionindex.CatalogueSnapshot{CommissionID: id, Status: "active", CatalogueVersion: 2},
		statistics: commissionindex.StatisticsSnapshot{CommissionID: id, StatisticsVersion: 3},
	}
	readyIndex := indexFake{document: commissionindex.Document{CommissionID: id, Active: true, CatalogueVersion: 2, StatisticsVersion: 3}, found: true}
	tests := []struct {
		name       string
		projection projectionFake
		source     sourceFake
		index      indexFake
		failed     func(Diagnostic) bool
	}{
		{"Redis", projectionFake{err: errors.New("redis payload secret")}, readySource, readyIndex, func(value Diagnostic) bool {
			return value.ProjectionStatus == "unavailable" && value.CatalogueStatus == "ok" && value.StatisticsStatus == "ok" && value.ESStatus == "ok"
		}},
		{"catalogue", readyProjection, sourceFake{catalogueErr: errors.New("source text secret"), statistics: readySource.statistics}, readyIndex, func(value Diagnostic) bool {
			return value.ProjectionStatus == "ok" && value.CatalogueStatus == "unavailable" && value.StatisticsStatus == "ok" && value.ESStatus == "ok"
		}},
		{"statistics", readyProjection, sourceFake{catalogue: readySource.catalogue, statisticsErr: errors.New("profile secret")}, readyIndex, func(value Diagnostic) bool {
			return value.ProjectionStatus == "ok" && value.CatalogueStatus == "ok" && value.StatisticsStatus == "unavailable" && value.ESStatus == "ok"
		}},
		{"ES", readyProjection, readySource, indexFake{err: errors.New("vector secret")}, func(value Diagnostic) bool {
			return value.ProjectionStatus == "ok" && value.CatalogueStatus == "ok" && value.StatisticsStatus == "ok" && value.ESStatus == "unavailable"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			failed, err := NewService(tc.projection, tc.source, tc.index, embeddingFake{provider: "ollama", dimensions: 768})
			if err != nil {
				t.Fatal(err)
			}
			got, err := failed.Diagnostic(context.Background(), id)

			if err != nil || !tc.failed(got) {
				t.Fatalf("diagnostic=%#v error=%v", got, err)
			}
			encoded, err := json.Marshal(got)
			if err != nil || strings.Contains(string(encoded), "secret") {
				t.Fatalf("unsafe diagnostic=%s error=%v", encoded, err)
			}
		})
	}
}
func TestDiagnosticSlowProjectionDoesNotEraseHealthyStageEvidence(t *testing.T) {
	const id int64 = 42
	service, err := NewService(
		deadlineProjection{},
		contextAwareSource{sourceFake: sourceFake{
			catalogue:  commissionindex.CatalogueSnapshot{CommissionID: id, Status: "active", CatalogueVersion: 2},
			statistics: commissionindex.StatisticsSnapshot{CommissionID: id, StatisticsVersion: 3},
		}},
		contextAwareIndex{indexFake: indexFake{document: commissionindex.Document{CommissionID: id, Active: true, CatalogueVersion: 2, StatisticsVersion: 3}, found: true}},
		embeddingFake{provider: "ollama", dimensions: 768},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got, err := service.Diagnostic(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectionStatus != "unavailable" || got.CatalogueStatus != "ok" || got.StatisticsStatus != "ok" || got.ESStatus != "ok" {
		t.Fatalf("diagnostic=%#v", got)
	}
}

func TestNewServiceRejectsMissingDependencies(t *testing.T) {
	if _, err := NewService(nil, sourceFake{}, indexFake{}, embeddingFake{}); err != ErrInvalidConfiguration {
		t.Fatalf("error=%v", err)
	}
}

type streamInspectorFake struct {
	info             StreamInfo
	groups           []GroupInfo
	dlq              []StreamMessage
	source           map[string][]StreamMessage
	err              error
	dlqCount         int64
	sourceRangeCount int
}

func (f *streamInspectorFake) Info(context.Context, string) (StreamInfo, error) {
	return f.info, f.err
}
func (f *streamInspectorFake) Groups(context.Context, string) ([]GroupInfo, error) {
	return f.groups, f.err
}
func (f *streamInspectorFake) Range(_ context.Context, stream, start, stop string, count int64, reverse bool) ([]StreamMessage, error) {
	if stream == "commission-dlq" {
		f.dlqCount = count
		if !reverse || start != "+" || stop != "-" {
			return nil, errors.New("unexpected DLQ range")
		}
		return f.dlq, f.err
	}
	if reverse || start != stop || count != 1 {
		return nil, errors.New("unexpected source range")
	}
	f.sourceRangeCount++
	return f.source[start], f.err
}

func commissionEnvelope(topic, aggregateType, aggregateID string) map[string]any {
	return map[string]any{
		"event_id": "event-1", "schema_version": "1", "topic": topic,
		"aggregate_type": aggregateType, "aggregate_id": aggregateID, "aggregate_version": "2",
		"occurred_at": "1", "payload_json": `{"aggregate_id":"payload-must-not-be-read"}`,
	}
}

func TestCommissionProjectionEnvelopeRequiresTopicAggregatePair(t *testing.T) {
	const id = "9223372036854775000"
	for _, tc := range []struct {
		topic, aggregateType string
		want                 bool
	}{
		{"commission.published.v1", "commission", true},
		{"commission.offline.v1", "commission", true},
		{"commission.statistics.changed.v1", "commission_statistics", true},
		{"commission.published.v1", "commission_statistics", false},
		{"commission.offline.v1", "commission_statistics", false},
		{"commission.statistics.changed.v1", "commission", false},
	} {
		got := commissionProjectionEnvelopeMatches(commissionEnvelope(tc.topic, tc.aggregateType, id), id)
		if got != tc.want {
			t.Fatalf("topic=%q aggregate_type=%q got=%v want=%v", tc.topic, tc.aggregateType, got, tc.want)
		}
	}
}

func TestRedisProjectionStateReadsGroupAndBoundedStructuredDLQMatches(t *testing.T) {
	inspector := &streamInspectorFake{
		info:   StreamInfo{LastID: "900-0", Length: 20},
		groups: []GroupInfo{{Name: "commission-group", Pending: 7, Lag: 3}},
		dlq: []StreamMessage{
			{Values: map[string]any{"original_stream": "commission-stream", "original_id": "10-0", "payload": `{"aggregate_id":"wrong-payload-id","secret":"do not parse"}`}},
			{Values: map[string]any{"original_stream": "commission-stream", "original_id": "11-0"}},
			{Values: map[string]any{"original_stream": "commission-stream", "original_id": "12-0"}},
			{Values: map[string]any{"original_stream": "commission-stream", "original_id": "13-0"}},
			{Values: map[string]any{"original_stream": "commission-stream", "original_id": "14-0"}},
			{Values: map[string]any{"original_stream": "commission-stream", "original_id": "15-0"}},
			{Values: map[string]any{"original_stream": "commission-stream", "original_id": "16-0"}},
			{Values: map[string]any{"original_stream": "other-stream", "original_id": "17-0"}},
		},
		source: map[string][]StreamMessage{
			"10-0": {{ID: "10-0", Values: commissionEnvelope("commission.published.v1", "commission", "9223372036854775000")}},
			"11-0": {{ID: "11-0", Values: commissionEnvelope("commission.offline.v1", "commission", "9223372036854775000")}},
			"12-0": {{ID: "12-0", Values: commissionEnvelope("commission.statistics.changed.v1", "commission_statistics", "9223372036854775000")}},
			"13-0": {{ID: "13-0", Values: commissionEnvelope("commission.published.v1", "commission_statistics", "9223372036854775000")}},
			"14-0": {{ID: "14-0", Values: commissionEnvelope("commission.statistics.changed.v1", "commission", "9223372036854775000")}},
			"15-0": {{ID: "15-0", Values: commissionEnvelope("order.created.v1", "commission", "9223372036854775000")}},
			"16-0": {{ID: "16-0", Values: commissionEnvelope("commission.offline.v1", "commission", "922337203685477500")}},
		},
	}
	state := newRedisProjectionState(inspector, "commission-stream", "commission-group", "commission-dlq")
	got, err := state.Diagnostic(context.Background(), 9223372036854775000)
	if err != nil {
		t.Fatal(err)
	}
	if got.StreamLastID != "900-0" || got.Pending != 7 || got.Lag != 3 || got.DLQMatches != 3 {
		t.Fatalf("diagnostic=%#v", got)
	}
	if inspector.dlqCount <= 0 || inspector.dlqCount > 256 {
		t.Fatalf("unbounded DLQ read count=%d", inspector.dlqCount)
	}
}

func TestRedisProjectionStateReadyFailsWithoutExactGroup(t *testing.T) {
	inspector := &streamInspectorFake{info: StreamInfo{LastID: "1-0"}, groups: []GroupInfo{{Name: "other"}}}
	state := newRedisProjectionState(inspector, "commission-stream", "commission-group", "commission-dlq")
	if err := state.Ready(context.Background()); err == nil {
		t.Fatal("missing consumer group reported ready")
	}
}

type embeddingClientFake struct {
	vector []float32
	err    error
	text   string
}

func (f *embeddingClientFake) GetEmbedding(_ context.Context, text string) ([]float32, error) {
	f.text = text
	return f.vector, f.err
}

func TestEmbeddingProbeVerifiesProviderAndDimensions(t *testing.T) {
	client := &embeddingClientFake{vector: make([]float32, 768)}
	probe := NewEmbeddingProbe("ollama", 768, client)
	provider, dimensions, err := probe.Ready(context.Background())
	if err != nil || provider != "ollama" || dimensions != 768 || client.text == "" {
		t.Fatalf("Ready() provider=%q dimensions=%d text=%q error=%v", provider, dimensions, client.text, err)
	}

	bad := NewEmbeddingProbe("ollama", 768, &embeddingClientFake{vector: make([]float32, 1536)})
	if _, _, err := bad.Ready(context.Background()); err == nil {
		t.Fatal("dimension mismatch reported ready")
	}
}

func TestRedisProjectionStateRejectsUnknownLag(t *testing.T) {
	inspector := &streamInspectorFake{
		info:   StreamInfo{LastID: "1-0"},
		groups: []GroupInfo{{Name: "commission-group", Pending: 0, Lag: -1}},
	}
	state := newRedisProjectionState(inspector, "commission-stream", "commission-group", "commission-dlq")
	if err := state.Ready(context.Background()); err == nil {
		t.Fatal("unknown lag reported ready")
	}
	if _, err := state.Diagnostic(context.Background(), 42); err == nil {
		t.Fatal("unknown lag returned diagnostics")
	}
}

func TestRedisProjectionStateValidatesCanonicalStreamIDs(t *testing.T) {
	for _, lastID := range []string{"", "1", "-1-0", "1--0", "01-0", "1-00", "1-a", strings.Repeat("1", 80) + "-0"} {
		inspector := &streamInspectorFake{info: StreamInfo{LastID: lastID}, groups: []GroupInfo{{Name: "commission-group", Lag: 0}}}
		state := newRedisProjectionState(inspector, "commission-stream", "commission-group", "commission-dlq")
		if err := state.Ready(context.Background()); err == nil {
			t.Fatalf("invalid stream ID %q reported ready", lastID)
		}
	}
}

func TestRedisProjectionStateSkipsMalformedDLQOriginalIDsWithoutRangeRead(t *testing.T) {
	inspector := &streamInspectorFake{
		info: StreamInfo{LastID: "2-0"}, groups: []GroupInfo{{Name: "commission-group", Lag: 0}},
		dlq: []StreamMessage{
			{Values: map[string]any{"original_stream": "commission-stream", "original_id": "bad"}},
			{Values: map[string]any{"original_stream": "commission-stream", "original_id": strings.Repeat("1", 80) + "-0"}},
		},
	}
	state := newRedisProjectionState(inspector, "commission-stream", "commission-group", "commission-dlq")
	got, err := state.Diagnostic(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.DLQMatches != 0 || inspector.sourceRangeCount != 0 {
		t.Fatalf("diagnostic=%#v source range reads=%d", got, inspector.sourceRangeCount)
	}
}

type countingEmbeddingClient struct {
	mu      sync.Mutex
	calls   int
	vector  []float32
	err     error
	started chan struct{}
	release chan struct{}
}

func (c *countingEmbeddingClient) GetEmbedding(context.Context, string) ([]float32, error) {
	c.mu.Lock()
	c.calls++
	started := c.started
	release := c.release
	c.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	return c.vector, c.err
}

func (c *countingEmbeddingClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestEmbeddingProbeCachesValidatedSuccessAndFailure(t *testing.T) {
	now := time.Unix(100, 0)
	clock := func() time.Time { return now }
	success := &countingEmbeddingClient{vector: make([]float32, 3)}
	probe := newEmbeddingProbeWithClock("ollama", 3, success, clock, time.Minute, time.Second)
	for range 2 {
		if _, _, err := probe.Ready(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if success.callCount() != 1 {
		t.Fatalf("success calls=%d", success.callCount())
	}
	now = now.Add(time.Minute)
	if _, _, err := probe.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if success.callCount() != 2 {
		t.Fatalf("success calls after TTL=%d", success.callCount())
	}

	failure := &countingEmbeddingClient{err: errors.New("provider unavailable")}
	failureProbe := newEmbeddingProbeWithClock("ollama", 3, failure, clock, time.Minute, time.Second)
	for range 2 {
		if _, _, err := failureProbe.Ready(context.Background()); err == nil {
			t.Fatal("failure reported ready")
		}
	}
	if failure.callCount() != 1 {
		t.Fatalf("failure calls=%d", failure.callCount())
	}
	now = now.Add(time.Second)
	if _, _, err := failureProbe.Ready(context.Background()); err == nil {
		t.Fatal("expired failure reported ready")
	}
	if failure.callCount() != 2 {
		t.Fatalf("failure calls after TTL=%d", failure.callCount())
	}
}

func TestEmbeddingProbeSingleflightsConcurrentReadiness(t *testing.T) {
	client := &countingEmbeddingClient{
		vector: make([]float32, 3), started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	probe := newEmbeddingProbeWithClock("ollama", 3, client, time.Now, time.Minute, time.Second)
	const callers = 20
	var wg sync.WaitGroup
	wg.Add(callers)
	errorsSeen := make(chan error, callers)
	for range callers {
		go func() {
			defer wg.Done()
			_, _, err := probe.Ready(context.Background())
			errorsSeen <- err
		}()
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("embedding request did not start")
	}
	close(client.release)
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if client.callCount() != 1 {
		t.Fatalf("concurrent embedding calls=%d", client.callCount())
	}
}
