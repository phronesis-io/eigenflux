package sort_test

import (
	"context"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/tests/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSynonymExpansion verifies that searching with a synonym term matches
// items indexed with the canonical form, via the synonym_search analyzer
// on keywords.text and domains.text subfields.
func TestSynonymExpansion(t *testing.T) {
	cfg := config.Load()
	db.Init(cfg.PgDSN)
	err := es.InitES(cfg.EmbeddingDimensions)
	require.NoError(t, err, "Failed to initialize Elasticsearch")

	ctx := context.Background()
	testutil.CleanTestData(t)

	now := time.Now()
	ts := now.UnixNano()

	// Index items with canonical keyword forms
	items := []sortDal.Item{
		{
			ID:        ts + 1,
			Content:   "New VC fund launched for AI startups",
			Summary:   "New VC fund launched for AI startups",
			Type:      "info",
			Keywords:  []string{"venture-capital", "ai-infra"},
			Domains:   []string{"venture-capital"},
			UpdatedAt: now,
			CreatedAt: now,
		},
		{
			ID:        ts + 2,
			Content:   "Web3 protocol upgrade announced",
			Summary:   "Web3 protocol upgrade announced",
			Type:      "info",
			Keywords:  []string{"web3", "defi"},
			Domains:   []string{"web3"},
			UpdatedAt: now,
			CreatedAt: now,
		},
		{
			ID:        ts + 3,
			Content:   "Kubernetes security best practices",
			Summary:   "Kubernetes security best practices",
			Type:      "info",
			Keywords:  []string{"kubernetes", "cloud-security"},
			Domains:   []string{"devops"},
			UpdatedAt: now,
			CreatedAt: now,
		},
		{
			ID:        ts + 4,
			Content:   "Agent systems for enterprise automation",
			Summary:   "Agent systems for enterprise automation",
			Type:      "info",
			Keywords:  []string{"agent-systems"},
			Domains:   []string{"ai-agents"},
			UpdatedAt: now,
			CreatedAt: now,
		},
	}

	for i := range items {
		require.NoError(t, sortDal.IndexItem(ctx, &items[i]), "Failed to index item %d", i)
	}

	testutil.RefreshES(t)

	cases := []struct {
		name          string
		searchKeyword string
		expectItemIDs []int64
		description   string
	}{
		{
			name:          "vc matches venture-capital",
			searchKeyword: "vc",
			expectItemIDs: []int64{ts + 1},
			description:   "Searching 'vc' should match item with 'venture-capital' keyword via synonym",
		},
		{
			name:          "crypto matches web3",
			searchKeyword: "crypto",
			expectItemIDs: []int64{ts + 2},
			description:   "Searching 'crypto' should match item with 'web3' keyword via synonym",
		},
		{
			name:          "blockchain matches web3",
			searchKeyword: "blockchain",
			expectItemIDs: []int64{ts + 2},
			description:   "Searching 'blockchain' should match item with 'web3' keyword via synonym",
		},
		{
			name:          "k8s matches kubernetes",
			searchKeyword: "k8s",
			expectItemIDs: []int64{ts + 3},
			description:   "Searching 'k8s' should match item with 'kubernetes' keyword via synonym",
		},
		{
			name:          "ai-agents matches agent-systems",
			searchKeyword: "ai-agents",
			expectItemIDs: []int64{ts + 4},
			description:   "Searching 'ai-agents' should match item with 'agent-systems' keyword/domain via synonym",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := sortDal.SearchItems(ctx, &sortDal.SearchItemsRequest{
				Keywords: []string{tc.searchKeyword},
				Limit:    50,
			})
			require.NoError(t, err)

			idSet := make(map[int64]bool)
			for _, it := range resp.Items {
				idSet[it.ID] = true
			}

			t.Logf("Search keyword: %s → returned %d items", tc.searchKeyword, len(resp.Items))
			for _, it := range resp.Items {
				t.Logf("  ID=%d keywords=%v domains=%v score=%.4f", it.ID, it.Keywords, it.Domains, it.Score)
			}

			for _, expectedID := range tc.expectItemIDs {
				assert.True(t, idSet[expectedID], "%s (expected item %d)", tc.description, expectedID)
			}
		})
	}
}

// TestSynonymExactMatchStillWorks verifies that the synonym configuration does
// not break exact term matching on the keyword field (boost 3.0 path).
func TestSynonymExactMatchStillWorks(t *testing.T) {
	cfg := config.Load()
	db.Init(cfg.PgDSN)
	err := es.InitES(cfg.EmbeddingDimensions)
	require.NoError(t, err, "Failed to initialize Elasticsearch")

	ctx := context.Background()
	testutil.CleanTestData(t)

	now := time.Now()
	ts := now.UnixNano()

	item := &sortDal.Item{
		ID:        ts + 1,
		Content:   "Exact match test item",
		Summary:   "Exact match test item",
		Type:      "info",
		Keywords:  []string{"venture-capital"},
		Domains:   []string{"fintech"},
		UpdatedAt: now,
		CreatedAt: now,
	}
	require.NoError(t, sortDal.IndexItem(ctx, item))
	testutil.RefreshES(t)

	// Exact keyword should still match via the term query path
	resp, err := sortDal.SearchItems(ctx, &sortDal.SearchItemsRequest{
		Keywords: []string{"venture-capital"},
		Limit:    10,
	})
	require.NoError(t, err)

	found := false
	for _, it := range resp.Items {
		if it.ID == item.ID {
			found = true
			break
		}
	}
	assert.True(t, found, "Exact keyword match should still work after synonym configuration")
}
