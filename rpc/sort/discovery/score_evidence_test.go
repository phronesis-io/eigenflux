package discovery

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

type scoredChannelSource struct {
	*sourceFake
	lexical, dense Document
}

func (s scoredChannelSource) Recall(_ context.Context, _ Context, _ Kind, channel string, _ int) ([]Document, error) {
	switch channel {
	case "lexical":
		return []Document{s.lexical}, nil
	case "dense":
		return []Document{s.dense}, nil
	default:
		return nil, nil
	}
}

func TestDenseScoreMergeRequiresSameProjection(t *testing.T) {
	for _, mismatch := range []string{"", "index", "source_version", "projection_version"} {
		t.Run(mismatch, func(t *testing.T) {
			e, source, _ := engineFixture()
			e.Compiler.Embedder = embedStub{}
			lexical := baseDoc(Agent)
			lexical.SourceIndex, lexical.Version, lexical.ProjectionVersion = "agents-v2", "7", 2
			dense := lexical
			score := .9
			dense.Lexical, dense.DenseScore = 0, &score
			switch mismatch {
			case "index":
				dense.SourceIndex = "agents-v3"
			case "source_version":
				dense.Version = "8"
			case "projection_version":
				dense.ProjectionVersion = 3
			}
			e.Sources = scoredChannelSource{sourceFake: source, lexical: lexical, dense: dense}
			result, err := e.Execute(context.Background(), 1, Request{Query: "landing design", SourceKinds: []Kind{Agent}}, Search, 100)
			require.NoError(t, err)
			require.Len(t, result.Candidates, 1)
			if mismatch == "" {
				require.InDelta(t, .8, result.Candidates[0].Score.Features["cosine"], 1e-6)
			} else {
				require.Contains(t, result.Candidates[0].Score.Missing, "semantic")
			}
		})
	}
}

func TestAllKindsUseESScoreWithoutForwardVector(t *testing.T) {
	rule := Rule{Version: "retrieval-v2", BM25Scale: 1, CosineFloor: 0, MinRelevance: 0, Threshold: 0, HalfLifeMS: 1000}
	for _, kind := range []Kind{Broadcast, Agent, Commission} {
		c := baseContext()
		d := baseDoc(kind)
		score := .9
		d.DenseScore = &score
		got := ScoreRules(c, d, rule, 100)
		require.InDelta(t, .8, got.Features["cosine"], 1e-6)
		require.InDelta(t, .55*(10.0/11)+.45*.8, got.Relevance, 1e-6)
		require.NotContains(t, got.Features, "slot")
		d.DenseScore = nil
		got = ScoreRules(c, d, rule, 100)
		require.Contains(t, got.Missing, "semantic")
		require.InDelta(t, 10.0/11, got.Relevance, 1e-6)
		for _, v := range []float64{math.NaN(), math.Inf(1), -1, 2} {
			d.DenseScore = &v
			require.Contains(t, ScoreRules(c, d, rule, 100).Missing, "semantic")
		}
		d.Lexical = 0
		score = 1
		d.DenseScore = &score
		require.InDelta(t, .45, ScoreRules(c, d, rule, 100).Relevance, 1e-6, "dense-only evidence remains useful")
	}
}
func TestRemovedFiltersAndChannelsAreRejected(t *testing.T) {
	for _, field := range []string{"category", "subtype", "taxonomy_version", "intents"} {
		value := `"design"`
		if field == "intents" {
			value = `["design"]`
		}
		_, err := Decode[Request]([]byte(`{"query":"design","filters":{"` + field + `":` + value + `}}`))
		require.Error(t, err, "removed constraints must not be silently dropped")
	}
	for _, channel := range []string{"synonym", "structured"} {
		_, err := Query(baseContext(), Agent, channel, 10)
		require.Error(t, err)
	}
}
