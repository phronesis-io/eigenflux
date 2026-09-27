package discovery

import (
	"context"
	"errors"
	"testing"

	"eigenflux_server/rpc/sort/discovery/needembedding"
	"eigenflux_server/rpc/sort/discovery/queryprocessing"
	"github.com/stretchr/testify/require"
)

type cacheLookupFunc func(context.Context, int64, string, string) ([]float32, error)

func (f cacheLookupFunc) Lookup(ctx context.Context, id int64, text, version string) ([]float32, error) {
	return f(ctx, id, text, version)
}

type countingEmbed struct{ calls int }

func (e *countingEmbed) GetEmbedding(context.Context, string) ([]float32, error) {
	e.calls++
	return []float32{1, 0}, nil
}
func TestSavedNeedsUseOnlyAsyncVectors(t *testing.T) {
	e, _, _ := engineFixture()
	embed := &countingEmbed{}
	e.Compiler.Embedder = embed
	for _, tc := range []struct {
		err     error
		warning string
	}{{needembedding.ErrPending, "embedding_pending"}, {errors.New("cache offline"), "embedding_unavailable"}, {nil, ""}} {
		e.Compiler.NeedVectors = cacheLookupFunc(func(_ context.Context, id int64, text, version string) ([]float32, error) {
			require.EqualValues(t, 7, id)
			require.Equal(t, "design", text)
			require.Equal(t, queryprocessing.Version, version)
			if tc.err != nil {
				return nil, tc.err
			}
			return []float32{1, 0}, nil
		})
		c, err := e.Compiler.Need(context.Background(), 1, 2, 100, capturedFixture(7, Agent))
		require.NoError(t, err)
		if tc.warning != "" {
			require.Contains(t, c.Warnings, tc.warning)
			require.Empty(t, c.Vector)
		} else {
			require.Equal(t, []float32{1, 0}, c.Vector)
		}
	}
	require.Zero(t, embed.calls, "online stored Need called model")
	_, err := e.Compiler.Need(context.Background(), 1, 2, 100, capturedFixture(0, Agent))
	require.NoError(t, err)
	require.Equal(t, 1, embed.calls, "inline Need should still compute on demand")
	_, err = e.Compiler.Query(context.Background(), 1, 2, 100, Request{Query: "design", SourceKinds: []Kind{Agent}}, "query")
	require.NoError(t, err)
	require.Equal(t, 2, embed.calls, "explicit query path changed")
}

func TestOwnerContextNeverCallsEmbeddingOrCache(t *testing.T) {
	embed := &countingEmbed{}
	cc := Compiler{Embedder: embed, NeedVectors: cacheLookupFunc(func(context.Context, int64, string, string) ([]float32, error) {
		t.Fatal("owner context must not schedule Need vector work")
		return nil, nil
	})}
	for _, text := range []string{"design", "  Ｋ８Ｓ 运维", "寻找服务", "four", "five"} {
		c, err := cc.Query(context.Background(), 1, 2, 100, Request{Query: text, SourceKinds: AllKinds}, "agent_context")
		require.NoError(t, err)
		require.Empty(t, c.Vector)
		require.Empty(t, c.Warnings, "intentional lexical-only fallback is not a failed model call")
		require.NotNil(t, c.QueryAnalysis)
		require.NotEmpty(t, c.SpecHash)
	}
	require.Zero(t, embed.calls)
}
