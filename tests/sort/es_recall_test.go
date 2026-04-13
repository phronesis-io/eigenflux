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

// TestESRecallEmbeddingReturned verifies that items with embeddings have their
// embedding field populated in search results.
func TestESRecallEmbeddingReturned(t *testing.T) {
	cfg := config.Load()
	db.Init(cfg.PgDSN)
	err := es.InitES(cfg.EmbeddingDimensions)
	require.NoError(t, err, "Failed to initialize Elasticsearch")

	ctx := context.Background()
	testutil.CleanTestData(t)

	embedding := make([]float32, cfg.EmbeddingDimensions)
	for i := range embedding {
		embedding[i] = float32(i) * 0.001
	}

	now := time.Now()
	itemID := now.UnixNano()
	item := &sortDal.Item{
		ID:        itemID,
		Content:   "Test item with embedding",
		Summary:   "Test item with embedding",
		Type:      "info",
		Keywords:  []string{"embedding", "test"},
		Domains:   []string{"tech"},
		Embedding: embedding,
		UpdatedAt: now,
		CreatedAt: now,
	}
	err = sortDal.IndexItem(ctx, item)
	require.NoError(t, err, "Failed to index item")

	testutil.RefreshES(t)

	resp, err := sortDal.SearchItems(ctx, &sortDal.SearchItemsRequest{
		Keywords: []string{"embedding"},
		Limit:    10,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Items, "Expected at least one item")

	var found *sortDal.Item
	for i := range resp.Items {
		if resp.Items[i].ID == itemID {
			found = &resp.Items[i]
			break
		}
	}
	require.NotNil(t, found, "Indexed item should be in search results")
	assert.NotEmpty(t, found.Embedding, "Embedding should be returned in search results")
	assert.Equal(t, cfg.EmbeddingDimensions, len(found.Embedding), "Embedding dimension should match")
}

// TestESRecallGeoCountryFilter verifies that geo_country hard filter works correctly:
// - Items with matching geo_country are returned
// - Items without geo_country (global) are returned
// - Items with a different geo_country are excluded
func TestESRecallGeoCountryFilter(t *testing.T) {
	cfg := config.Load()
	db.Init(cfg.PgDSN)
	err := es.InitES(cfg.EmbeddingDimensions)
	require.NoError(t, err, "Failed to initialize Elasticsearch")

	ctx := context.Background()
	testutil.CleanTestData(t)

	now := time.Now()
	ts := now.UnixNano()

	jpItem := &sortDal.Item{
		ID:         ts + 1,
		Content:    "Japan local news",
		Summary:    "Japan local news",
		Type:       "info",
		Keywords:   []string{"japan", "news"},
		Domains:    []string{"news"},
		GeoCountry: "JP",
		UpdatedAt:  now,
		CreatedAt:  now,
	}
	usItem := &sortDal.Item{
		ID:         ts + 2,
		Content:    "US market update",
		Summary:    "US market update",
		Type:       "info",
		Keywords:   []string{"us", "market"},
		Domains:    []string{"finance"},
		GeoCountry: "US",
		UpdatedAt:  now,
		CreatedAt:  now,
	}
	globalItem := &sortDal.Item{
		ID:        ts + 3,
		Content:   "Global tech trends",
		Summary:   "Global tech trends",
		Type:      "info",
		Keywords:  []string{"tech", "global"},
		Domains:   []string{"tech"},
		UpdatedAt: now,
		CreatedAt: now,
	}

	require.NoError(t, sortDal.IndexItem(ctx, jpItem))
	require.NoError(t, sortDal.IndexItem(ctx, usItem))
	require.NoError(t, sortDal.IndexItem(ctx, globalItem))

	testutil.RefreshES(t)

	// Query with GeoCountry=JP: should return JP item + global item, not US item
	resp, err := sortDal.SearchItems(ctx, &sortDal.SearchItemsRequest{
		GeoCountry: "JP",
		Limit:      50,
	})
	require.NoError(t, err)

	idSet := make(map[int64]bool)
	for _, it := range resp.Items {
		idSet[it.ID] = true
	}

	assert.True(t, idSet[jpItem.ID], "JP item should be returned when GeoCountry=JP")
	assert.True(t, idSet[globalItem.ID], "Global item (no geo_country) should always be returned")
	assert.False(t, idSet[usItem.ID], "US item should be excluded when GeoCountry=JP")
}

// TestESRecallPartialMatch verifies that with minimum_should_match=0, an item matching
// only 1 out of 2 requested keywords is still returned.
func TestESRecallPartialMatch(t *testing.T) {
	cfg := config.Load()
	db.Init(cfg.PgDSN)
	err := es.InitES(cfg.EmbeddingDimensions)
	require.NoError(t, err, "Failed to initialize Elasticsearch")

	ctx := context.Background()
	testutil.CleanTestData(t)

	now := time.Now()
	ts := now.UnixNano()

	// Item matching keyword "alpha" only
	alphaItem := &sortDal.Item{
		ID:        ts + 1,
		Content:   "Alpha only item",
		Summary:   "Alpha only item",
		Type:      "info",
		Keywords:  []string{"alpha"},
		Domains:   []string{"tech"},
		UpdatedAt: now,
		CreatedAt: now,
	}
	// Item matching keyword "beta" only
	betaItem := &sortDal.Item{
		ID:        ts + 2,
		Content:   "Beta only item",
		Summary:   "Beta only item",
		Type:      "info",
		Keywords:  []string{"beta"},
		Domains:   []string{"tech"},
		UpdatedAt: now,
		CreatedAt: now,
	}
	// Item matching both keywords
	bothItem := &sortDal.Item{
		ID:        ts + 3,
		Content:   "Alpha and beta item",
		Summary:   "Alpha and beta item",
		Type:      "info",
		Keywords:  []string{"alpha", "beta"},
		Domains:   []string{"tech"},
		UpdatedAt: now,
		CreatedAt: now,
	}

	require.NoError(t, sortDal.IndexItem(ctx, alphaItem))
	require.NoError(t, sortDal.IndexItem(ctx, betaItem))
	require.NoError(t, sortDal.IndexItem(ctx, bothItem))

	testutil.RefreshES(t)

	// Search with both keywords; with min_should_match=0 all three should be returned
	resp, err := sortDal.SearchItems(ctx, &sortDal.SearchItemsRequest{
		Keywords: []string{"alpha", "beta"},
		Limit:    50,
	})
	require.NoError(t, err)

	idSet := make(map[int64]bool)
	for _, it := range resp.Items {
		idSet[it.ID] = true
	}

	assert.True(t, idSet[alphaItem.ID], "Item matching only 'alpha' should be returned with min_should_match=0")
	assert.True(t, idSet[betaItem.ID], "Item matching only 'beta' should be returned with min_should_match=0")
	assert.True(t, idSet[bothItem.ID], "Item matching both keywords should always be returned")
}
