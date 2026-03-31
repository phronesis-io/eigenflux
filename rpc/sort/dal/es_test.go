package dal

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/es"
)

func refreshTestES(t *testing.T) {
	t.Helper()

	esPort := os.Getenv("ELASTICSEARCH_HTTP_PORT")
	if esPort == "" {
		esPort = "9200"
	}

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/items-*/_refresh", esPort), nil)
	if err != nil {
		t.Fatalf("Failed to create ES refresh request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to refresh ES indices: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		t.Fatalf("Unexpected ES refresh status: %s", resp.Status)
	}
}

// TestESIntegration tests the Elasticsearch integration
// Run with: go test -v ./rpc/sort/dal/ -run TestESIntegration
// Requires: Elasticsearch running on localhost:9200
func TestESIntegration(t *testing.T) {
	cfg := config.Load()

	// Initialize ES
	if err := es.InitES(cfg.EmbeddingDimensions); err != nil {
		t.Skipf("Elasticsearch not available, skipping integration test: %v", err)
	}

	ctx := context.Background()

	// Test data
	testItem := &Item{
		ID:         999999,
		Content:    "This is a test article about artificial intelligence and machine learning",
		Extra:      map[string]interface{}{"test": true},
		RawURL:     "https://example.com/test",
		Summary:    "AI and machine learning test",
		Type:       "info",
		Domains:    []string{"AI", "technology", "machine-learning"},
		Geo:        "Beijing, China",
		SourceType: "original",
		Keywords:   []string{"AI", "machine learning", "deep learning"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	// Test indexing
	t.Run("IndexItem", func(t *testing.T) {
		err := IndexItem(ctx, testItem)
		if err != nil {
			t.Fatalf("Failed to index item: %v", err)
		}
		t.Log("Item indexed successfully")
	})

	// Force refresh so the indexed document is immediately searchable.
	refreshTestES(t)

	// Test search - by keywords
	t.Run("SearchByKeywords", func(t *testing.T) {
		req := &SearchItemsRequest{
			Keywords: []string{"AI", "machine learning"},
			Limit:    10,
		}

		resp, err := SearchItems(ctx, req)
		if err != nil {
			t.Fatalf("Failed to search items: %v", err)
		}

		if len(resp.Items) == 0 {
			t.Error("Expected at least 1 item, got 0")
		}

		found := false
		for _, item := range resp.Items {
			if item.ID == testItem.ID {
				found = true
				t.Logf("Found test item: ID=%d, Summary=%s", item.ID, item.Summary)
			}
		}

		if !found {
			t.Error("Test item not found in search results")
		}
	})

	// Test search - by domains
	t.Run("SearchByDomains", func(t *testing.T) {
		req := &SearchItemsRequest{
			Domains: []string{"AI", "technology"},
			Limit:   10,
		}

		resp, err := SearchItems(ctx, req)
		if err != nil {
			t.Fatalf("Failed to search items: %v", err)
		}

		if len(resp.Items) == 0 {
			t.Error("Expected at least 1 item, got 0")
		}

		t.Logf("Found %d items with domains AI or technology", len(resp.Items))
	})

	// Test search - by geo
	t.Run("SearchByGeo", func(t *testing.T) {
		req := &SearchItemsRequest{
			Geo:   "Beijing",
			Limit: 10,
		}

		resp, err := SearchItems(ctx, req)
		if err != nil {
			t.Fatalf("Failed to search items: %v", err)
		}

		t.Logf("Found %d items with geo matching Beijing", len(resp.Items))
	})

	// Test expire time filtering
	t.Run("ExpireTimeFilter", func(t *testing.T) {
		// Create an expired item
		expiredTime := time.Now().Add(-1 * time.Hour)
		expiredItem := &Item{
			ID:         999998,
			Content:    "Expired test article",
			Summary:    "Expiration test",
			Type:       "info",
			Domains:    []string{"test"},
			Keywords:   []string{"expired"},
			ExpireTime: &expiredTime,
			SourceType: "original",
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}

		// Index expired item
		if err := IndexItem(ctx, expiredItem); err != nil {
			t.Fatalf("Failed to index expired item: %v", err)
		}

		refreshTestES(t)

		// Search, should not include expired item
		req := &SearchItemsRequest{
			Keywords: []string{"expired"},
			Limit:    10,
		}

		resp, err := SearchItems(ctx, req)
		if err != nil {
			t.Fatalf("Failed to search items: %v", err)
		}

		// Check if expired item is included
		for _, item := range resp.Items {
			if item.ID == expiredItem.ID {
				t.Error("Expired item should not be in search results")
			}
		}

		t.Log("Expired items correctly filtered out")
	})

	// Clean up test data
	t.Run("Cleanup", func(t *testing.T) {
		if err := DeleteItem(ctx, testItem.ID); err != nil {
			t.Logf("Warning: Failed to delete test item: %v", err)
		}
		if err := DeleteItem(ctx, 999998); err != nil {
			t.Logf("Warning: Failed to delete expired test item: %v", err)
		}
		t.Log("Test data cleaned up")
	})
}
