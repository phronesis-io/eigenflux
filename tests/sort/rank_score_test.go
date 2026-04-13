package sort_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/ranker"
	"eigenflux_server/tests/testutil"

	"github.com/stretchr/testify/require"
)

// TestRankScoreBreakdown uses e2e-style items to print detailed score breakdowns
// for each (user, item) pair, making it easy to evaluate parameter settings.
func TestRankScoreBreakdown(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping in short mode")
	}

	cfg := config.Load()
	db.Init(cfg.PgDSN)
	require.NoError(t, es.InitES(cfg.EmbeddingDimensions))
	ctx := context.Background()
	testutil.CleanTestData(t)

	ts := time.Now().UnixNano()

	// --- Items from e2e test ---
	type testItem struct {
		content  string
		keywords []string
		domains  []string
		itemType string
	}
	items := []testItem{
		{
			content:  "Google DeepMind released Gemini 2.0, a next-generation multimodal AI model with mixture-of-experts architecture that reduces inference costs by 60%",
			keywords: []string{"AI", "multimodal", "deep learning", "language model"},
			domains:  []string{"tech", "AI"},
			itemType: "info",
		},
		{
			content:  "Kubernetes 1.32 with priority-based preemption reducing scheduling latency by 40% in 10k+ node clusters, and updated HPA with custom metrics",
			keywords: []string{"kubernetes", "distributed systems", "cloud computing", "microservices"},
			domains:  []string{"tech", "cloud"},
			itemType: "info",
		},
		{
			content:  "Traditional Tuscan pasta-making techniques: hand-rolled pici, pappardelle, tagliatelle with flour selection and regional sauce pairings",
			keywords: []string{"cooking", "pasta", "Italian cuisine", "food heritage"},
			domains:  []string{"food", "lifestyle"},
			itemType: "info",
		},
		{
			content:  "RAG techniques study: semantic chunking with overlap produces 35% better recall than fixed-size; hybrid dense+sparse retrieval for enterprise QA",
			keywords: []string{"RAG", "NLP", "machine learning", "information retrieval"},
			domains:  []string{"tech", "AI"},
			itemType: "info",
		},
		{
			content:  "Urgent: Senior Go backend developer needed for distributed storage project, competitive salary, remote OK, deadline in 2 weeks",
			keywords: []string{"Go", "distributed systems", "backend", "hiring"},
			domains:  []string{"tech", "jobs"},
			itemType: "demand",
		},
	}

	// Add a demand item with near expiry to test urgency
	expireTime := time.Now().Add(12 * time.Hour)

	// Index items to ES
	esItems := make([]sortDal.Item, len(items))
	for i, item := range items {
		now := time.Now()
		id := ts + int64(i)*1000

		esItem := sortDal.Item{
			ID:        id,
			Content:   item.content,
			Summary:   item.content,
			Type:      item.itemType,
			Keywords:  item.keywords,
			Domains:   item.domains,
			GroupID:   id,
			UpdatedAt: now,
			CreatedAt: now,
		}
		if item.itemType == "demand" {
			esItem.ExpireTime = &expireTime
		}
		esItems[i] = esItem

		err := sortDal.IndexItem(ctx, &esItem)
		require.NoError(t, err, "Failed to index item %d", i)
	}
	testutil.RefreshES(t)

	// --- User profiles ---
	type testProfile struct {
		name     string
		keywords []string
		domains  []string
	}
	profiles := []testProfile{
		{
			name:     "AI Researcher",
			keywords: []string{"artificial intelligence", "machine learning", "large language models", "distributed systems"},
			domains:  []string{"tech", "AI"},
		},
		{
			name:     "DevOps Engineer",
			keywords: []string{"kubernetes", "cloud computing", "microservices", "Go"},
			domains:  []string{"tech", "cloud"},
		},
		{
			name:     "Food Blogger",
			keywords: []string{"cooking", "Italian cuisine", "recipes", "food heritage"},
			domains:  []string{"food", "lifestyle"},
		},
	}

	rankerCfg := ranker.NewRankerConfig(cfg)
	r := ranker.New(rankerCfg)

	// Print config
	t.Logf("=== Ranker Config ===")
	t.Logf("  Alpha (semantic):  %.2f", rankerCfg.Alpha)
	t.Logf("  Beta  (keyword):   %.2f", rankerCfg.Beta)
	t.Logf("  Gamma (freshness): %.2f", rankerCfg.Gamma)
	t.Logf("  Delta (diversity): %.2f", rankerCfg.Delta)
	t.Logf("  UrgencyBoost:      %.2f", rankerCfg.UrgencyBoost)
	t.Logf("  DraftDampening:    %.2f", rankerCfg.DraftDampening)
	t.Logf("")

	for _, prof := range profiles {
		userProfile := &ranker.UserProfile{
			Keywords: prof.keywords,
			Domains:  prof.domains,
		}

		ranked := r.Rank(esItems, userProfile, len(esItems))

		t.Logf("=== %s (keywords: %s) ===", prof.name, strings.Join(prof.keywords, ", "))
		t.Logf("%-4s %-80s %-10s %-10s %-10s %-10s %-7s",
			"Rank", "Item (truncated)", "Semantic", "Keyword", "Freshness", "Total", "Draft?")
		t.Logf("%s", strings.Repeat("-", 135))

		for i, ri := range ranked {
			// Find the item content
			var content string
			for _, item := range esItems {
				if item.ID == ri.ItemID {
					content = item.Content
					break
				}
			}
			if len(content) > 78 {
				content = content[:78] + ".."
			}
			draft := ""
			if ri.Scores.IsDraft {
				draft = "YES"
			}
			t.Logf("%-4d %-80s %-10.4f %-10.4f %-10.4f %-10.4f %-7s",
				i+1, content, ri.Scores.Semantic, ri.Scores.Keyword, ri.Scores.Freshness, ri.Scores.Total, draft)
		}
		t.Logf("")

		// Sanity check: top item should match user's domain
		if len(ranked) > 0 {
			topItem := ranked[0]
			for _, item := range esItems {
				if item.ID == topItem.ItemID {
					t.Logf("  >> Top pick: %s", fmt.Sprintf("%.60s", item.Content))
					t.Logf("     Score breakdown: semantic=%.4f keyword=%.4f freshness=%.4f → total=%.4f",
						topItem.Scores.Semantic, topItem.Scores.Keyword, topItem.Scores.Freshness, topItem.Scores.Total)
					break
				}
			}
		}
		t.Logf("")
	}
}
