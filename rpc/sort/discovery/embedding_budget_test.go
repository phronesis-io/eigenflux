package discovery

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type stalledOnlineEmbedding struct{ calls atomic.Int64 }

func (s *stalledOnlineEmbedding) GetEmbedding(ctx context.Context, _ string) ([]float32, error) {
	s.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s *stalledOnlineEmbedding) Lookup(ctx context.Context, _ int64, _, _ string) ([]float32, error) {
	return s.GetEmbedding(ctx, "")
}

type deadlineSource struct {
	*sourceFake
	lexical atomic.Int64
}

func (s *deadlineSource) Recall(ctx context.Context, c Context, k Kind, ch string, limit int) ([]Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ch == "lexical" {
		s.lexical.Add(1)
	}
	return s.sourceFake.Recall(ctx, c, k, ch, limit)
}

func TestSlowEmbeddingLeavesBudgetForLexicalResults(t *testing.T) {
	for _, input := range []string{"query", "inline", "saved"} {
		t.Run(input, func(t *testing.T) {
			e, source, store := engineFixture()
			source.docs = []Document{baseDoc(Broadcast)}
			observed := &deadlineSource{sourceFake: source}
			e.Sources = observed
			stalled := &stalledOnlineEmbedding{}
			e.Compiler.Embedder = stalled
			req := Request{Query: "design", SourceKinds: []Kind{Broadcast}}
			if input != "query" {
				n := capturedFixture(7, Broadcast)
				req.Query = ""
				if input == "saved" {
					store.needs[7] = n
					req.NeedID = 7
					e.Compiler.NeedVectors = stalled
				} else {
					in, err := n.ExecutionInput()
					require.NoError(t, err)
					req.Need = &in
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			started := time.Now()
			x, err := e.Execute(ctx, 1, req, Search, 100)
			require.NoError(t, err)
			require.GreaterOrEqual(t, time.Since(started), 2*time.Second, "embedding must not be limited to a fraction of the request deadline")
			require.NoError(t, ctx.Err(), "optional embedding consumed the parent deadline")
			require.Len(t, x.Candidates, 1)
			require.Contains(t, x.PartialReasons, "embedding_unavailable")
			require.Positive(t, observed.lexical.Load())
			require.EqualValues(t, 1, stalled.calls.Load())
		})
	}
}

func TestOnlineEmbeddingHasCapWithoutParentDeadline(t *testing.T) {
	stalled := &stalledOnlineEmbedding{}
	cc := Compiler{Embedder: stalled}
	started := time.Now()
	c, err := cc.Query(context.Background(), 1, 2, 100, Request{Query: "design", SourceKinds: []Kind{Broadcast}}, "query")
	require.NoError(t, err)
	require.Contains(t, c.Warnings, "embedding_unavailable")
	require.GreaterOrEqual(t, time.Since(started), 2*time.Second)
	require.Less(t, time.Since(started), 4*time.Second)
}

func TestOnlineEmbeddingHonorsShorterParentDeadline(t *testing.T) {
	stalled := &stalledOnlineEmbedding{}
	cc := Compiler{Embedder: stalled}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := cc.Query(ctx, 1, 2, 100, Request{Query: "design", SourceKinds: []Kind{Broadcast}}, "query")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
	require.EqualValues(t, 1, stalled.calls.Load())
}

func TestOnlineEmbeddingDoesNotHideCallerCancellation(t *testing.T) {
	stalled := &stalledOnlineEmbedding{}
	cc := Compiler{Embedder: stalled}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cc.Query(ctx, 1, 2, 100, Request{Query: "design", SourceKinds: []Kind{Broadcast}}, "query")
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, stalled.calls.Load())
}
