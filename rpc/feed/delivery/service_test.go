package delivery

import (
	"context"
	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/replaylog"
	"eigenflux_server/rpc/sort/discovery"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"
)

type ids struct{ n atomic.Int64 }

func (i *ids) NextID() (int64, error) { return i.n.Add(1), nil }

type executor struct {
	x     discovery.Execution
	calls int
	err   error
}

func (e *executor) Execute(_ context.Context, _ int64, _ discovery.Request, m discovery.Mode, _ int64) (discovery.Execution, error) {
	e.calls++
	e.x.Mode = m
	return e.x, e.err
}
func setup(t *testing.T) (Service, *executor, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { r.Close() })
	now := time.Now().UnixMilli()
	c := discovery.Context{ID: 44, OwnerID: 1, Revision: 1, State: "active", Origin: "query", Kinds: discovery.AllKinds, CreatedAt: now}
	x := discovery.Execution{Contexts: []discovery.Context{c}, Status: "ok"}
	for _, kind := range discovery.AllKinds {
		x.Candidates = append(x.Candidates, discovery.Candidate{Context: c, Document: discovery.Document{Ref: discovery.SourceRef{Type: kind, ID: 77}, GroupID: 88, AuthorID: 2, Preview: "result"}, Score: discovery.Score{Value: .9, Eligible: true, Version: "test", ScorerType: "rules"}})
	}
	e := &executor{x: x}
	return Service{Redis: r, IDs: &ids{}, Executor: e}, e, r
}
func TestSearchRecordingAndIdempotency(t *testing.T) {
	s, e, r := setup(t)
	ctx := context.Background()
	request := discovery.Request{Query: "test"}
	a, err := s.Serve(ctx, 1, request, discovery.Search, "key")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Serve(ctx, 1, request, discovery.Search, "key")
	if err != nil || a.ImpressionID != b.ImpressionID || e.calls != 1 {
		t.Fatalf("retry: %#v %v calls=%d", b, err, e.calls)
	}
	if r.Exists(ctx, "impr:agent:1:items", bloomfilter.GetKeyForDate(time.Now()), "impr:discovery:agent:1:items").Val() != 0 {
		t.Fatal("search polluted automatic history")
	}
	await(t, func() bool {
		return r.SIsMember(ctx, "impr:search:agent:1:items", "77").Val() && r.XLen(ctx, replaylog.StreamName).Val() == 1
	})
	if !r.SIsMember(ctx, "impr:search:agent:1:items", "77").Val() {
		t.Fatal("missing feedback validation")
	}
	entries, err := r.XRange(ctx, replaylog.StreamName, "-", "+").Result()
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	var rows []replaylog.ServedItem
	if err = json.Unmarshal([]byte(entries[0].Values["items"].(string)), &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if err = row.Validate(); err != nil {
			t.Fatal(err)
		}
		if row.SourceKind != "broadcast" && row.ItemID != 0 {
			t.Fatal("typed ID collision")
		}
	}
	_, err = s.Serve(ctx, 1, discovery.Request{Query: "different"}, discovery.Search, "key")
	if err == nil {
		t.Fatal("missing conflict")
	}
	e.err = discovery.Failure(503, "source_unavailable")
	replay, err := s.Serve(ctx, 1, request, discovery.Search, "key")
	if err != nil || replay.ImpressionID != a.ImpressionID || e.calls != 1 {
		t.Fatal("assembled response must replay without source reads", replay, err, e.calls)
	}
	if _, err := s.Serve(ctx, 1, request, discovery.Search, "new-key"); err == nil {
		t.Fatal("fresh request must still query its source")
	}
	if r.XLen(ctx, replaylog.StreamName).Val() != 1 {
		t.Fatal("retry recorded another exposure")
	}
}
func TestAutomaticWritesExistingHistory(t *testing.T) {
	s, _, r := setup(t)
	ctx := context.Background()
	_, err := s.Serve(ctx, 1, discovery.Request{}, discovery.Recommendation, "")
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		return r.SIsMember(ctx, "impr:discovery:agent:1:items", "agent:77").Val() && r.XLen(ctx, replaylog.StreamName).Val() == 1
	})
	for _, pair := range [][2]string{{"impr:agent:1:items", "77"}, {"impr:agent:1:groups", "88"}, {bloomfilter.GetKeyForDate(time.Now()), "1:88"}, {"impr:discovery:agent:1:items", "commission:77"}, {"impr:discovery:agent:1:items", "agent:77"}} {
		if !r.SIsMember(ctx, pair[0], pair[1]).Val() {
			t.Fatal(pair)
		}
	}
	if r.Exists(ctx, "impr:search:agent:1:items").Val() != 0 {
		t.Fatal("automatic in search history")
	}
}
func TestSampleFailureDoesNotBlockResponseOrHistory(t *testing.T) {
	s, e, r := setup(t)
	ctx := context.Background()
	r.Set(ctx, replaylog.StreamName, "wrong", 0)
	before := recordingFailures("sample")
	request := discovery.Request{Query: "x"}
	out, err := s.Serve(ctx, 1, request, discovery.Search, "key")
	if err != nil || len(out.Items) != 3 {
		t.Fatal(out, err)
	}
	await(t, func() bool {
		return r.SIsMember(ctx, "impr:search:agent:1:items", "77").Val() && recordingFailures("sample") > before
	})
	again, err := s.Serve(ctx, 1, request, discovery.Search, "key")
	if err != nil || again.ImpressionID != out.ImpressionID || e.calls != 1 {
		t.Fatal(again, err)
	}
}

func TestHistoryFailureDoesNotBlockResponseOrSample(t *testing.T) {
	s, _, r := setup(t)
	ctx := context.Background()
	r.Set(ctx, "impr:search:agent:1:items", "wrong", 0)
	before := recordingFailures("history")
	out, err := s.Serve(ctx, 1, discovery.Request{Query: "x"}, discovery.Search, "key")
	if err != nil || len(out.Items) != 3 {
		t.Fatal(out, err)
	}
	await(t, func() bool {
		return r.XLen(ctx, replaylog.StreamName).Val() == 1 && recordingFailures("history") > before
	})
}

func await(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("background recording did not complete")
}

func recordingFailures(stage string) float64 {
	m := &dto.Metric{}
	_ = metrics.DiscoveryRecordingFailures.WithLabelValues(stage).Write(m)
	return m.GetCounter().GetValue()
}

// Pause both recording writers while allowing response cache commands through.
type recordingGate struct {
	release chan struct{}
	started chan struct{}
}

func (g recordingGate) DialHook(next redis.DialHook) redis.DialHook { return next }
func (g recordingGate) wait(ctx context.Context) error {
	g.started <- struct{}{}
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g recordingGate) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "xadd" {
			if err := g.wait(ctx); err != nil {
				return err
			}
		}
		return next(ctx, cmd)
	}
}
func (g recordingGate) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if cmd.Name() == "sadd" {
				if err := g.wait(ctx); err != nil {
					return err
				}
				break
			}
		}
		return next(ctx, cmds)
	}
}
func TestResponseDoesNotWaitForRecordingAndRequestCancellation(t *testing.T) {
	s, _, r := setup(t)
	gate := recordingGate{release: make(chan struct{}), started: make(chan struct{}, 2)}
	r.AddHook(gate)
	var release sync.Once
	defer release.Do(func() { close(gate.release) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Serve(ctx, 1, discovery.Request{Query: "x"}, discovery.Search, "key"); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("response waited for recording")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-gate.started:
		case <-time.After(time.Second):
			t.Fatal("writer did not start")
		}
	}
	cancel()
	release.Do(func() { close(gate.release) })
	await(t, func() bool {
		return r.XLen(context.Background(), replaylog.StreamName).Val() == 1 && r.SIsMember(context.Background(), "impr:search:agent:1:items", "77").Val()
	})
}

func TestExplicitIdempotencyRequiresCacheButUnkeyedDeliveryDoesNot(t *testing.T) {
	s, e, r := setup(t)
	_ = r.Close()
	ctx := context.Background()
	beforeHistory, beforeSample := recordingFailures("history"), recordingFailures("sample")
	out, err := s.Serve(ctx, 1, discovery.Request{Query: "x"}, discovery.Search, "")
	if err != nil || len(out.Items) != 3 {
		t.Fatal(out, err)
	}
	await(t, func() bool {
		return recordingFailures("history") > beforeHistory && recordingFailures("sample") > beforeSample
	})
	if _, err := s.Serve(ctx, 1, discovery.Request{Query: "x"}, discovery.Search, "key"); err == nil {
		t.Fatal("explicit idempotency cannot succeed without its cache")
	}
	if e.calls != 1 {
		t.Fatal("cache failure should not execute another search")
	}
}

func TestSamplePreservesRequestTimeAndRecordsDeliveryTime(t *testing.T) {
	_, e, _ := setup(t)
	e.x.Mode = discovery.Search
	requestTime := time.Now().Add(-time.Minute).UnixMilli()
	before := time.Now().UnixMilli()
	values, err := sampleValues(1, e.x, "impression", 0, requestTime)
	if err != nil {
		t.Fatal(err)
	}
	servedAt, err := strconv.ParseInt(values["served_at"].(string), 10, 64)
	if err != nil || servedAt < before {
		t.Fatal("delivery time replaced with request start", servedAt, err)
	}
	var features struct {
		Search struct {
			RequestTime int64 `json:"request_time"`
		} `json:"search_context"`
	}
	if err := json.Unmarshal([]byte(values["agent_features"].(string)), &features); err != nil {
		t.Fatal(err)
	}
	if features.Search.RequestTime != requestTime {
		t.Fatal("request snapshot timestamp changed")
	}
}
