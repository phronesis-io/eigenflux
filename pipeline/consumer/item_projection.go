package consumer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	itemDal "eigenflux_server/rpc/item/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
)

func waitMessageRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func persistedTags(csv string) []string {
	if csv == "" {
		return nil
	}
	return strings.Split(csv, ",")
}

// Completed SQL rows are the durable enrichment checkpoint. A failed search
// projection must retry from this checkpoint, never rerun LLM/dedup or inflate
// the best-effort homepage counters by publishing another snapshot.
func (c *ItemConsumer) resumeCompletedItem(ctx context.Context, raw *itemDal.RawItem, processed *itemDal.ProcessedItem) HandleResult {
	vector, err := c.embeddingClient.GetEmbedding(ctx, raw.RawContent)
	if err == nil && c.embeddingClient.Dimensions() > 0 && len(vector) != c.embeddingClient.Dimensions() {
		err = fmt.Errorf("embedding dimension mismatch: got=%d want=%d", len(vector), c.embeddingClient.Dimensions())
	}
	if err != nil {
		logger.Default().Warn("completed item embedding repair failed", "itemID", raw.ItemID, "err", err)
		return HandleRetry
	}
	item := &sortDal.Item{
		ID: raw.ItemID, AuthorAgentID: raw.AuthorAgentID, Content: raw.RawContent, RawURL: raw.RawURL,
		Summary: processed.Summary, Type: processed.BroadcastType, Domains: persistedTags(processed.Domains),
		Keywords: persistedTags(processed.Keywords), ExpireTime: parseExpireTime(processed.ExpireTime),
		Geo: processed.Geo, SourceType: processed.SourceType, ExpectedResponse: processed.ExpectedResponse,
		GroupID: processed.GroupID, QualityScore: processed.QualityScore, Lang: processed.Lang,
		Timeliness: processed.Timeliness, Embedding: vector, CreatedAt: time.UnixMilli(raw.CreatedAt), UpdatedAt: time.Now(),
	}
	slots, err := json.Marshal(searchindex.Slots{Lang: []string{item.Lang}})
	if err != nil {
		return HandleRetry
	}
	itemDB := db.DB.WithContext(ctx)
	if err := itemDB.Table("processed_items").Where("item_id = ?", raw.ItemID).Update("retrieval_slots", string(slots)).Error; err != nil {
		return HandleRetry
	}
	if c.enableFeatureIndex {
		if _, err := (featureindex.BroadcastIndex{DB: itemDB, Redis: mq.RDB}).Load(ctx, []int64{raw.ItemID}); err != nil {
			logger.Default().Error("broadcast feature materialization failed", "itemID", raw.ItemID, "err", err)
		}
	}
	if err := sortDal.IndexItem(ctx, item); err != nil {
		logger.Default().Warn("completed item search repair failed", "itemID", raw.ItemID, "err", err)
		return HandleRetry
	}
	logger.Default().Info("completed item search projection repaired", "itemID", raw.ItemID)
	return HandleSuccess
}
