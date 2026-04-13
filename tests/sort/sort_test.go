package sort_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	itemDal "eigenflux_server/rpc/item/dal"
	profileDal "eigenflux_server/rpc/profile/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/tests/testutil"

	"github.com/cloudwego/kitex/client"
	etcd "github.com/kitex-contrib/registry-etcd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
}

// TestSortService tests the sort service with real data
func TestSortService(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping sort service test in short mode")
	}

	cfg := config.Load()
	db.Init(cfg.PgDSN)
	err := es.InitES(cfg.EmbeddingDimensions)
	require.NoError(t, err, "Failed to initialize Elasticsearch")

	ctx := context.Background()
	ts := time.Now().UnixNano()

	// Clean residual data
	testutil.CleanTestData(t)

	// Create test agents with unique emails
	t.Log("=== Setup: Creating test agents ===")
	techEmail := fmt.Sprintf("sort_tech_%d@test.com", ts)
	bizEmail := fmt.Sprintf("sort_biz_%d@test.com", ts)

	techAgent := createTestAgent(t, techEmail, "Tech User", "Interested in AI, blockchain, and cloud computing")
	bizAgent := createTestAgent(t, bizEmail, "Business User", "Focus on startup, investment, and market trends")

	createTestProfile(t, techAgent.AgentID, []string{"AI", "blockchain", "cloud computing", "technology"})
	createTestProfile(t, bizAgent.AgentID, []string{"startup", "investment", "business", "market"})

	// Create test items and index to ES
	t.Log("=== Setup: Creating test items ===")
	testItems := []struct {
		content  string
		keywords []string
		domains  []string
	}{
		{
			content:  "New AI breakthrough in natural language processing",
			keywords: []string{"AI", "NLP", "machine learning", "technology"},
			domains:  []string{"tech", "AI"},
		},
		{
			content:  "Blockchain technology revolutionizes supply chain management",
			keywords: []string{"blockchain", "supply chain", "technology"},
			domains:  []string{"tech", "blockchain"},
		},
		{
			content:  "Startup funding reaches record high in Q1 2026",
			keywords: []string{"startup", "funding", "investment", "business"},
			domains:  []string{"business", "finance"},
		},
		{
			content:  "Cloud computing trends for enterprise applications",
			keywords: []string{"cloud computing", "enterprise", "technology"},
			domains:  []string{"tech", "cloud"},
		},
	}

	for _, item := range testItems {
		createTestItem(t, ctx, ts, techAgent.AgentID, item.content, item.keywords, item.domains)
	}

	// Force ES refresh instead of sleeping
	testutil.RefreshES(t)

	// Test sort service for tech user
	t.Run("TechUser_ReturnsMatchingItems", func(t *testing.T) {
		results := callSortService(t, cfg, techAgent.AgentID)
		require.NotEmpty(t, results, "Should return sorted items")

		items, err := itemDal.BatchGetProcessedItems(db.DB, results)
		require.NoError(t, err)

		for i, item := range items {
			t.Logf("  %d. %s (keywords: %s)", i+1, item.Summary, item.Keywords)
		}

		// First item should be tech-related
		require.NotEmpty(t, items)
		kws := strings.Split(items[0].Keywords, ",")
		hasMatch := false
		for _, kw := range kws {
			kw = strings.TrimSpace(kw)
			if kw == "AI" || kw == "blockchain" || kw == "cloud computing" || kw == "technology" {
				hasMatch = true
				break
			}
		}
		assert.True(t, hasMatch, "First item should match tech user's interests")
	})

	// Test sort service for business user
	t.Run("BusinessUser_ReturnsMatchingItems", func(t *testing.T) {
		results := callSortService(t, cfg, bizAgent.AgentID)
		require.NotEmpty(t, results, "Should return sorted items")

		items, err := itemDal.BatchGetProcessedItems(db.DB, results)
		require.NoError(t, err)

		for i, item := range items {
			t.Logf("  %d. %s (keywords: %s)", i+1, item.Summary, item.Keywords)
		}

		// Should contain business-related items
		require.NotEmpty(t, items)
		kws := strings.Split(items[0].Keywords, ",")
		hasMatch := false
		for _, kw := range kws {
			kw = strings.TrimSpace(kw)
			if kw == "startup" || kw == "investment" || kw == "business" || kw == "market" {
				hasMatch = true
				break
			}
		}
		assert.True(t, hasMatch, "First item should match business user's interests")
	})

	// Test sort returns correct count with limit
	t.Run("LimitRespected", func(t *testing.T) {
		resolver, err := etcd.NewEtcdResolver([]string{cfg.EtcdAddr})
		require.NoError(t, err)
		cli, err := sortservice.NewClient("SortService", client.WithResolver(resolver))
		require.NoError(t, err)

		limit := int32(2)
		resp, err := cli.SortItems(context.Background(), &sort.SortItemsReq{
			AgentId: techAgent.AgentID,
			Limit:   &limit,
		})
		require.NoError(t, err)
		require.Equal(t, int32(0), resp.BaseResp.Code)
		assert.LessOrEqual(t, len(resp.ItemIds), 2, "Should respect limit parameter")
	})

	// Test sort with unknown agent returns empty or gracefully handles
	t.Run("UnknownAgent_NoError", func(t *testing.T) {
		resolver, err := etcd.NewEtcdResolver([]string{cfg.EtcdAddr})
		require.NoError(t, err)
		cli, err := sortservice.NewClient("SortService", client.WithResolver(resolver))
		require.NoError(t, err)

		limit := int32(10)
		resp, err := cli.SortItems(context.Background(), &sort.SortItemsReq{
			AgentId: 999999999,
			Limit:   &limit,
		})
		require.NoError(t, err)
		// Should not error, just return empty or fallback results
		t.Logf("Unknown agent got %d items (code=%d)", len(resp.ItemIds), resp.BaseResp.Code)
	})

	// Cleanup
	t.Cleanup(func() {
		testutil.CleanupTestEmails(t, techEmail, bizEmail)
	})
}

func TestSortService_SemanticRanking(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping in short mode")
	}

	cfg := config.Load()
	db.Init(cfg.PgDSN)
	require.NoError(t, es.InitES(cfg.EmbeddingDimensions))
	ctx := context.Background()
	testutil.CleanTestData(t)

	// Create agent with profile
	email := fmt.Sprintf("sort_semantic_%d@test.com", time.Now().UnixNano())
	agent := createTestAgent(t, email, "Semantic User", "Expert in machine learning and neural networks")
	createTestProfile(t, agent.AgentID, []string{"machine learning", "neural networks", "AI"})

	// Create items — one matching profile keywords, one not
	now := time.Now()
	createTestItem(t, ctx, now.UnixNano(), agent.AgentID, "Deep learning advances in computer vision", []string{"AI", "deep learning", "machine learning"}, []string{"tech", "AI"})
	createTestItem(t, ctx, now.UnixNano(), agent.AgentID, "Cooking recipes for beginners", []string{"cooking", "recipes"}, []string{"food", "lifestyle"})

	testutil.RefreshES(t)

	// Call sort service
	results := callSortService(t, cfg, agent.AgentID)
	require.NotEmpty(t, results)
	t.Logf("Semantic ranking returned %d items", len(results))

	// Verify that the ML-related item ranks higher
	items, err := itemDal.BatchGetProcessedItems(db.DB, results)
	require.NoError(t, err)
	require.NotEmpty(t, items)

	// The first item should be the ML-related one (keyword overlap scoring)
	firstKws := strings.Split(items[0].Keywords, ",")
	hasMLMatch := false
	for _, kw := range firstKws {
		kw = strings.TrimSpace(kw)
		if kw == "AI" || kw == "deep learning" || kw == "machine learning" {
			hasMLMatch = true
			break
		}
	}
	assert.True(t, hasMLMatch, "First item should be ML-related due to keyword overlap with profile")

	t.Cleanup(func() {
		testutil.CleanupTestEmails(t, email)
	})
}

func createTestAgent(t *testing.T, email, name, bio string) *profileDal.Agent {
	t.Helper()

	agent := &profileDal.Agent{
		AgentID:   time.Now().UnixNano(),
		Email:     email,
		AgentName: name,
		Bio:       bio,
		CreatedAt: time.Now().UnixMilli(),
	}

	err := profileDal.CreateAgent(db.DB, agent)
	require.NoError(t, err, "Failed to create agent")

	t.Logf("Created agent: %s (ID: %d)", name, agent.AgentID)
	return agent
}

func createTestProfile(t *testing.T, agentID int64, keywords []string) {
	t.Helper()

	profile := &profileDal.AgentProfile{
		AgentID:   agentID,
		Keywords:  strings.Join(keywords, ","),
		Status:    3,
		UpdatedAt: time.Now().UnixMilli(),
	}

	err := profileDal.CreateAgentProfile(db.DB, profile)
	require.NoError(t, err, "Failed to create profile")
}

func createTestItem(t *testing.T, ctx context.Context, ts int64, authorID int64, content string, keywords, domains []string) int64 {
	t.Helper()

	itemID := time.Now().UnixNano()
	groupID := itemID

	rawItem := &itemDal.RawItem{
		ItemID:        itemID,
		AuthorAgentID: authorID,
		RawContent:    content,
		CreatedAt:     time.Now().UnixMilli(),
	}
	err := itemDal.CreateRawItem(db.DB, rawItem)
	require.NoError(t, err, "Failed to create raw item")

	processedItem := &itemDal.ProcessedItem{
		ItemID:        itemID,
		Status:        3,
		Summary:       content,
		BroadcastType: "info",
		Keywords:      strings.Join(keywords, ","),
		Domains:       strings.Join(domains, ","),
		GroupID:       groupID,
		UpdatedAt:     time.Now().UnixMilli(),
	}
	err = itemDal.CreateProcessedItem(db.DB, processedItem)
	require.NoError(t, err, "Failed to create processed item")

	now := time.Now()
	esItem := &sortDal.Item{
		ID:        itemID,
		Content:   content,
		Summary:   content,
		Type:      "info",
		Keywords:  keywords,
		Domains:   domains,
		GroupID:   groupID,
		UpdatedAt: now,
		CreatedAt: now,
	}
	err = sortDal.IndexItem(ctx, esItem)
	require.NoError(t, err, "Failed to index item to ES")

	t.Logf("Created item %d: %s", itemID, content)
	return itemID
}

func callSortService(t *testing.T, cfg *config.Config, agentID int64) []int64 {
	t.Helper()

	resolver, err := etcd.NewEtcdResolver([]string{cfg.EtcdAddr})
	require.NoError(t, err, "Failed to create etcd resolver")

	cli, err := sortservice.NewClient("SortService", client.WithResolver(resolver))
	require.NoError(t, err, "Failed to create sort service client")

	limit := int32(10)
	resp, err := cli.SortItems(context.Background(), &sort.SortItemsReq{
		AgentId: agentID,
		Limit:   &limit,
	})
	require.NoError(t, err, "Failed to call SortItems")
	require.Equal(t, int32(0), resp.BaseResp.Code, "SortItems returned error: %s", resp.BaseResp.Msg)

	t.Logf("Sort returned %d items for agent %d", len(resp.ItemIds), agentID)
	return resp.ItemIds
}
