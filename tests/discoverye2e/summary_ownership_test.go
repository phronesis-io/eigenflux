package discoverye2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pipeline/consumer"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/itemdispatch"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/summary"
	itemdal "eigenflux_server/rpc/item/dal"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// This opt-in journey uses real HTTP/RPC, the outbox/Redis queue, the configured
// model, PostgreSQL and ES. All business writes target isolated local fixtures.
func TestPublisherSummaryPublicationJourney(t *testing.T) {
	if os.Getenv("SUMMARY_LIVE_MODEL_E2E") != "1" {
		t.Skip("SUMMARY_LIVE_MODEL_E2E=1 and test model access required")
	}
	s := startStack(t, map[string]string{"UGC_PUBLISHER_SUMMARY": "true", "UGC_SLOP_ENABLED": "true", "UGC_SLOP_ROLLOUT_BPS": "10000"})
	token := s.seedSession(t, s.author, "{broadcast:write,feed:read,profile:read}")
	var initial int64
	require.NoError(t, s.db.Table("raw_items").Where("author_agent_id=?", s.author).Count(&initial).Error)
	status, rejected, err := s.request("POST", "/api/v2/broadcasts", token, "", map[string]any{"content": "Redis Streams recovery keeps unfinished work pending.", "notes": `{"summary":123}`})
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Equal(t, 400, rejected.Code)
	var after int64
	require.NoError(t, s.db.Table("raw_items").Where("author_agent_id=?", s.author).Count(&after).Error)
	require.Equal(t, initial, after, "invalid summary must create no publication")
	groupErr := mq.RDB.XGroupCreateMkStream(context.Background(), "stream:item:publish", "cg:item:publish", "0").Err()
	require.True(t, groupErr == nil || strings.Contains(groupErr.Error(), "BUSYGROUP"))
	ids := []int64{}
	retryIDs := []string{}
	t.Cleanup(func() {
		for _, id := range ids {
			s.call(t, "DELETE", fmt.Sprintf("/api/v2/broadcasts/%d", id), token, "", nil, 200)
		}
	})
	for i, notes := range []string{`{"summary":"  FTC: Redis pending entries survive a worker restart  "}`, `{}`} {
		content := fmt.Sprintf("Redis Streams recovery procedure %d. A consumer group retains entries in its pending-entry list until acknowledgment. After a worker crash, inspect XPENDING, claim idle entries with XAUTOCLAIM, retry the unfinished operation with an idempotency key, and acknowledge only after committing its result. A failed downstream write must leave the entry pending. This procedure describes recovery behavior, not a claim of exactly-once external effects. Canary tests must include a crash between commit and acknowledgment. Fixture %s.", i, s.label)
		published := decode[struct {
			ItemID string `json:"item_id"`
		}](t, s.call(t, "POST", "/api/v2/broadcasts", token, "", map[string]any{"content": content, "notes": notes}, 200))
		id, e := strconv.ParseInt(published.ItemID, 10, 64)
		require.NoError(t, e)
		ids = append(ids, id)
	}
	// Worker startup after publication and after disabling the publication flag
	// exercises durable ownership rather than a process-local switch assumption.
	t.Setenv("UGC_PUBLISHER_SUMMARY", "false")
	cfg := config.Load()
	prompts, err := llm.LoadPrompts(filepath.Join(s.root, "static/templates/prompts"))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatchDone := make(chan struct{})
		go func() { defer close(dispatchDone); itemdispatch.Run(ctx, s.db, mq.RDB) }()
		consumer.NewItemConsumer(cfg, prompts).Start(ctx)
		<-dispatchDone
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("item worker failed to stop")
		}
	})
	for i, id := range ids {
		var row itemdal.ProcessedItem
		require.Eventually(t, func() bool {
			err = s.db.First(&row, "item_id=?", id).Error
			return err == nil && (row.Status == 3 || row.Status == 4 || row.Status == 2)
		}, 3*time.Minute, 200*time.Millisecond)
		require.Equal(t, int16(3), row.Status, "model admission must succeed; inspect worker logs")
		want, origin := "  FTC: Redis pending entries survive a worker restart  ", "publisher"
		if i == 1 {
			want = ""
			origin = "none"
		}
		require.Equal(t, want, row.Summary)
		require.Equal(t, origin, row.SummaryOrigin)
		response := decode[map[string]any](t, s.call(t, "GET", fmt.Sprintf("/api/v2/items/%d", id), token, "", nil, 200))
		detail, ok := response["item"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, origin, detail["summary_origin"])
		if i == 0 {
			require.Equal(t, want, detail["summary"])
		} else {
			require.Empty(t, detail["summary"])
		}
		// Completed duplicate delivery must preserve source text and attribution.
		retryID, addErr := mq.RDB.XAdd(context.Background(), &redis.XAddArgs{Stream: "stream:item:publish", Values: map[string]any{"item_id": strconv.FormatInt(id, 10)}}).Result()
		require.NoError(t, addErr)
		retryIDs = append(retryIDs, retryID)
		b, err := json.Marshal(detail)
		require.NoError(t, err)
		t.Logf("verified source=%s detail_bytes=%d", origin, len(b))
	}
	require.Eventually(t, func() bool {
		groups, e := mq.RDB.XInfoGroups(context.Background(), "stream:item:publish").Result()
		if e != nil {
			return false
		}
		acked := false
		for _, g := range groups {
			if g.Name == "cg:item:publish" && g.Pending == 0 && len(retryIDs) > 0 && g.LastDeliveredID == retryIDs[len(retryIDs)-1] {
				acked = true
			}
		}
		if !acked {
			return false
		}
		for _, id := range ids {
			var r itemdal.ProcessedItem
			if s.db.First(&r, "item_id=?", id).Error != nil || r.Status != 3 {
				return false
			}
		}
		return true
	}, 10*time.Second, 100*time.Millisecond)
}

// A fresh process set exercises operator switches against already accepted data.
func TestPublisherSummaryRollbackJourney(t *testing.T) {
	s := startStack(t, map[string]string{"UGC_PUBLISHER_SUMMARY": "false", "SUMMARY_TRANSLATIONS_DISABLED": "true"})
	token := s.seedSession(t, s.author, "{broadcast:write,feed:read,profile:read}")
	original := "FTC keeps this author text"
	require.NoError(t, s.db.Table("processed_items").Where("item_id=?", s.item).Updates(map[string]any{"summary": original, "summary_origin": "publisher", "summary_zh_literal": "测试译文", "summary_zh_key": summary.TranslationKey(original)}).Error)
	response := decode[map[string]any](t, s.call(t, "GET", fmt.Sprintf("/api/v2/items/%d", s.item), token, "", nil, 200))
	detail := response["item"].(map[string]any)
	require.Equal(t, original, detail["summary"])
	require.Equal(t, "publisher", detail["summary_origin"])
	require.Empty(t, detail["summary_zh"])
	var row itemdal.ProcessedItem
	require.NoError(t, s.db.First(&row, "item_id=?", s.item).Error)
	require.Equal(t, original, row.Summary)
	// Disabling new ownership must not erase existing source or cached data.
	var cached string
	require.NoError(t, s.db.Table("processed_items").Select("summary_zh_literal").Where("item_id=?", s.item).Scan(&cached).Error)
	require.Equal(t, "测试译文", cached)
}
