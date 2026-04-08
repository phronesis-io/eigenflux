package pipeline_test

import (
	"context"
	"math"
	"testing"

	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbedding tests the embedding client with similarity calculations
func TestEmbedding(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping embedding test in short mode")
	}

	cfg := config.Load()

	// Skip if embedding is not configured
	if cfg.EmbeddingProvider == "" {
		t.Skip("Embedding provider not configured")
	}

	t.Logf("Testing embedding provider: %s", cfg.EmbeddingProvider)
	if cfg.EmbeddingBaseURL != "" {
		t.Logf("Base URL: %s", cfg.EmbeddingBaseURL)
	}
	if cfg.EmbeddingModel != "" {
		t.Logf("Model: %s", cfg.EmbeddingModel)
	}

	client := embedding.NewClient(
		cfg.EmbeddingProvider,
		cfg.EmbeddingApiKey,
		cfg.EmbeddingBaseURL,
		cfg.EmbeddingModel,
		cfg.EmbeddingDimensions,
	)
	ctx := context.Background()

	texts := []struct {
		label string
		text  string
	}{
		{"AI/NLP (1)", "AI breakthrough in natural language processing: new transformer model achieves state-of-the-art performance"},
		{"AI/NLP (2)", "New advancement in NLP technology: deep learning architecture surpasses previous records on language understanding"},
		{"Finance", "Stock market analysis: tech stocks rose 3% today driven by strong quarterly earnings from semiconductor companies"},
		{"Weather", "Today's weather forecast: sunny skies expected with temperatures reaching 25 degrees Celsius"},
	}

	// Generate embeddings
	t.Log("Generating embeddings...")
	embeddings := make([][]float32, len(texts))
	for i, txt := range texts {
		emb, err := client.GetEmbedding(ctx, txt.text)
		require.NoError(t, err, "Failed to generate embedding for %s", txt.label)
		require.NotEmpty(t, emb, "Embedding should not be empty")
		if cfg.EmbeddingDimensions > 0 {
			assert.Equal(t, cfg.EmbeddingDimensions, len(emb), "Embedding dimension should match configuration")
		}

		embeddings[i] = emb
		t.Logf("  ✓ [%s] dimension=%d", txt.label, len(emb))
	}

	// Verify all embeddings have the same dimension
	dim := len(embeddings[0])
	for i, emb := range embeddings {
		assert.Equal(t, dim, len(emb), "All embeddings should have the same dimension (text %d)", i)
	}

	// Calculate and verify similarity matrix
	t.Log("Calculating similarity matrix...")
	similarities := make([][]float32, len(texts))
	for i := range texts {
		similarities[i] = make([]float32, len(texts))
		for j := range texts {
			similarities[i][j] = cosineSimilarity(embeddings[i], embeddings[j])
		}
	}

	// Log similarity matrix
	t.Log("Similarity matrix (cosine similarity):")
	for i, txt1 := range texts {
		for j, txt2 := range texts {
			t.Logf("  [%s] <-> [%s]: %.4f", txt1.label, txt2.label, similarities[i][j])
		}
	}

	// Verify diagonal is 1.0 (self-similarity)
	for i := range texts {
		assert.InDelta(t, 1.0, similarities[i][i], 0.01, "Self-similarity should be ~1.0")
	}

	// Verify AI/NLP texts are highly similar
	sim_ai_nlp := similarities[0][1]
	assert.Greater(t, sim_ai_nlp, float32(0.65), "AI/NLP texts should be highly similar (>0.65)")
	t.Logf("✓ AI/NLP similarity: %.4f", sim_ai_nlp)

	// Verify AI/NLP vs Weather are dissimilar
	sim_ai_weather := similarities[0][3]
	assert.Less(t, sim_ai_weather, float32(0.6), "AI/NLP vs Weather should be dissimilar (<0.6)")
	t.Logf("✓ AI/NLP vs Weather similarity: %.4f", sim_ai_weather)

	// Test grouping with threshold
	threshold := float32(0.85)
	t.Logf("Testing group assignment (threshold=%.2f)...", threshold)

	groups := make([]string, len(texts))
	for i, txt := range texts {
		groupID := "group_" + txt.label
		// Check against all previously assigned items
		for j := 0; j < i; j++ {
			sim := similarities[i][j]
			if sim >= threshold {
				groupID = groups[j]
				t.Logf("  [%s] similar to [%s] (%.4f >= %.2f) → %s",
					txt.label, texts[j].label, sim, threshold, groupID)
				break
			}
		}
		groups[i] = groupID
		t.Logf("  [%s] → %s", txt.label, groupID)
	}

	// Verify AI/NLP texts share the same group if similarity >= threshold
	if sim_ai_nlp >= threshold {
		assert.Equal(t, groups[0], groups[1], "AI/NLP texts should share the same group")
		t.Log("✓ AI/NLP texts correctly grouped together")
	}

	t.Log("✅ Embedding test completed successfully")
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}
