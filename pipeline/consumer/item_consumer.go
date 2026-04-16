package consumer

import (
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/dedup"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/stats"
	itemDal "eigenflux_server/rpc/item/dal"
	profileDal "eigenflux_server/rpc/profile/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
)

const (
	itemStream = "stream:item:publish"
	itemGroup  = "cg:item:publish"
)

var (
	updateProcessedItem       = itemDal.UpdateProcessedItem
	updateProcessedItemStatus = itemDal.UpdateProcessedItemStatus
	ackItemMessage            = mq.Ack
)

type ItemConsumer struct {
	llmClient        *llm.Client
	embeddingClient  *embedding.Client
	qualityThreshold float64
	maxWorkers       int
}

func NewItemConsumer(cfg *config.Config, prompts *llm.PromptRegistry) *ItemConsumer {
	return &ItemConsumer{
		llmClient:        llm.NewClient(cfg, prompts),
		embeddingClient:  embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions),
		qualityThreshold: cfg.QualityThreshold,
		maxWorkers:       cfg.ItemConsumerWorkers,
	}
}

func (c *ItemConsumer) Start(ctx context.Context) {
	logger.Default().Info("ItemConsumer starting", "workers", c.maxWorkers, "qualityThreshold", c.qualityThreshold)

	if err := mq.EnsureConsumerGroup(ctx, itemStream, itemGroup); err != nil {
		logger.Default().Error("ItemConsumer failed to create consumer group", "err", err)
		os.Exit(1)
	}

	// Create message channel for worker pool
	type msgTask struct {
		id     string
		values map[string]interface{}
	}
	msgChan := make(chan msgTask, c.maxWorkers*2)
	var wg sync.WaitGroup

	// Start worker pool
	for i := 0; i < c.maxWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			logger.Default().Info("ItemConsumer worker started", "workerID", workerID)
			for task := range msgChan {
				c.processMessage(ctx, task.id, task.values)
			}
			logger.Default().Info("ItemConsumer worker stopped", "workerID", workerID)
		}(i)
	}

	// Main loop: fetch messages and distribute to workers
	go func() {
		for {
			select {
			case <-ctx.Done():
				logger.Default().Info("ItemConsumer context cancelled, closing message channel")
				close(msgChan)
				return
			default:
			}

			msgs, err := mq.Consume(ctx, itemStream, itemGroup, "item-worker-1", 10)
			if err != nil {
				logger.Default().Error("ItemConsumer consume error", "err", err)
				time.Sleep(time.Second)
				continue
			}

			for _, msg := range msgs {
				task := msgTask{
					id:     msg.ID,
					values: msg.Values,
				}
				select {
				case msgChan <- task:
					// Message sent to worker
				case <-ctx.Done():
					logger.Default().Info("ItemConsumer context cancelled while sending message")
					close(msgChan)
					return
				}
			}
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Default().Info("ItemConsumer shutting down, waiting for workers to finish...")
	wg.Wait()
	logger.Default().Info("ItemConsumer all workers stopped")
}

func (c *ItemConsumer) processMessage(ctx context.Context, msgID string, values map[string]interface{}) {
	itemIDStr, ok := values["item_id"].(string)
	if !ok {
		logger.Default().Warn("ItemConsumer invalid message: missing item_id")
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("ItemConsumer invalid item_id", "itemID", itemIDStr)
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	logger.Default().Info("ItemConsumer processing item", "itemID", itemID)

	// Preserve publisher's expected_response if set to "no_reply"
	publisherExpResp, _ := itemDal.GetProcessedItemExpectedResponse(db.DB, itemID)

	// Set status to processing
	itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusProcessing)

	// Get raw item
	raw, err := itemDal.GetRawItemByID(db.DB, itemID)
	if err != nil {
		logger.Default().Warn("raw item not found", "itemID", itemID, "err", err)
		itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusFailed)
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	// Check blacklist keywords (cheap string match — run first)
	if matched := checkBlacklist(ctx, raw.RawContent, raw.RawURL, raw.RawNotes); matched != "" {
		logger.Default().Info("item discarded by blacklist keyword", "itemID", itemID, "keyword", matched)
		if err := itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusDiscarded); err != nil {
			logger.Default().Error("failed to update discard status", "itemID", itemID, "err", err)
			return
		}
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	// --- Dedup phase (cheap gates before expensive LLM calls) ---

	// Phase 1: Hash-based deduplication (Redis lookup — exact duplicates are discarded)
	contentHash := dedup.ComputeContentHash(raw.RawContent)
	logger.Default().Debug("ItemConsumer content hash", "itemID", itemID, "hash", contentHash)

	if hashExists, _, err := dedup.CheckHashExists(ctx, mq.RDB, contentHash); err == nil && hashExists {
		logger.Default().Info("ItemConsumer exact duplicate (hash match), discarding", "itemID", itemID)
		if err := itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusDiscarded); err != nil {
			logger.Default().Error("failed to update discard status", "itemID", itemID, "err", err)
			return
		}
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	} else if err != nil {
		logger.Default().Warn("ItemConsumer Redis hash check failed, continuing", "itemID", itemID, "err", err)
	}

	// Generate embedding (needed for both vector dedup and ES indexing)
	var itemEmbedding []float32
	var finalGroupID int64
	embAttempt := 0
	for embAttempt < maxRetries {
		embAttempt++
		var embErr error
		itemEmbedding, embErr = c.embeddingClient.GetEmbedding(ctx, raw.RawContent)
		if embErr == nil {
			if expectedDims := c.embeddingClient.Dimensions(); expectedDims > 0 && len(itemEmbedding) != expectedDims {
				embErr = fmt.Errorf("embedding dimension mismatch: got=%d want=%d", len(itemEmbedding), expectedDims)
				itemEmbedding = nil
			}
		}
		if embErr == nil {
			break
		}
		logger.Default().Warn("ItemConsumer embedding attempt failed", "attempt", embAttempt, "maxRetries", maxRetries, "itemID", itemID, "err", embErr)
		if embAttempt < maxRetries {
			time.Sleep(time.Duration(embAttempt) * time.Second)
		}
	}

	// Phase 2: Vector-based deduplication (assigns default group_id using info-mode rules)
	// similarItems is preserved for post-LLM broadcast_type-specific correction
	var similarItems []sortDal.Item
	if itemEmbedding == nil {
		logger.Default().Warn("ItemConsumer all embedding attempts failed, using item_id as group_id", "itemID", itemID)
		finalGroupID = itemID
	} else {
		var err error
		similarItems, err = sortDal.SearchSimilarItems(ctx, itemEmbedding, simThreshold, 5)
		if err != nil {
			logger.Default().Warn("ItemConsumer similarity search failed", "itemID", itemID, "err", err)
			finalGroupID = itemID
		} else {
			finalGroupID = assignDefaultGroupID(itemID, similarItems)
			if finalGroupID != itemID {
				logger.Default().Info("ItemConsumer item matched to group (default)", "itemID", itemID, "groupID", finalGroupID)
			} else {
				logger.Default().Info("ItemConsumer item is unique, creating new group", "itemID", itemID, "groupID", finalGroupID)
			}
		}
	}

	// Save hash for future exact-duplicate detection
	if err := dedup.SaveHash(ctx, mq.RDB, contentHash, finalGroupID); err != nil {
		logger.Default().Warn("ItemConsumer failed to save hash", "itemID", itemID, "err", err)
	}

	// --- LLM phase (expensive calls, run after cheap filters) ---

	// Safety check
	var safetyResult *llm.SafetyResult
	for attempt := 1; attempt <= maxRetries; attempt++ {
		safetyResult, err = c.llmClient.CheckSafety(ctx, raw.RawContent, raw.RawNotes)
		if err == nil {
			break
		}
		logger.Default().Warn("ItemConsumer safety check attempt failed", "attempt", attempt, "maxRetries", maxRetries, "itemID", itemID, "err", err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	if err != nil {
		logger.Default().Error("ItemConsumer safety check all retries failed", "itemID", itemID, "err", err)
		itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusFailed)
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}
	if !safetyResult.Safe {
		logger.Default().Info("item flagged by safety check", "itemID", itemID, "flag", safetyResult.Flag, "reason", safetyResult.Reason)
		if err := itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusDiscarded); err != nil {
			logger.Default().Error("failed to update discard status", "itemID", itemID, "err", err)
			return
		}
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	// --- Draft Indexing: make item discoverable immediately after safety check ---
	draftItem := &sortDal.Item{
		ID:            itemID,
		AuthorAgentID: raw.AuthorAgentID,
		Content:       raw.RawContent,
		RawURL:        raw.RawURL,
		GroupID:       finalGroupID,
		Embedding:     itemEmbedding,
		CreatedAt:     time.Unix(raw.CreatedAt/1000, 0),
		UpdatedAt:     time.Now(),
	}
	if err := sortDal.IndexItem(ctx, draftItem); err != nil {
		logger.Default().Warn("ItemConsumer failed to index draft item", "itemID", itemID, "err", err)
	} else {
		logger.Default().Info("ItemConsumer draft item indexed to ES", "itemID", itemID)
	}

	// LLM processing with retries
	var result *llm.ExtractResult
	for attempt := 1; attempt <= maxRetries; attempt++ {
		result, err = c.llmClient.ProcessItem(ctx, raw.RawContent, raw.RawNotes)
		if err == nil {
			break
		}
		logger.Default().Warn("ItemConsumer LLM attempt failed", "attempt", attempt, "maxRetries", maxRetries, "itemID", itemID, "err", err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	if err != nil {
		logger.Default().Error("all retries failed", "itemID", itemID, "err", err)
		itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusFailed)
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	// Check discard flag
	if result.Discard {
		logger.Default().Info("item discarded by LLM", "itemID", itemID, "reason", result.DiscardReason)
		if delErr := sortDal.DeleteItem(ctx, itemID); delErr != nil {
			logger.Default().Warn("ItemConsumer failed to remove draft from ES", "itemID", itemID, "err", delErr)
		}
		if err := itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusDiscarded); err != nil {
			logger.Default().Error("failed to update discard status", "itemID", itemID, "err", err)
			return
		}
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	// Check quality threshold
	if result.Quality < c.qualityThreshold {
		logger.Default().Info("item quality below threshold, discarding", "itemID", itemID, "quality", result.Quality, "threshold", c.qualityThreshold)
		if delErr := sortDal.DeleteItem(ctx, itemID); delErr != nil {
			logger.Default().Warn("ItemConsumer failed to remove draft from ES", "itemID", itemID, "err", delErr)
		}
		if err := itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusDiscarded); err != nil {
			logger.Default().Error("failed to update discard status", "itemID", itemID, "err", err)
			return
		}
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	logger.Default().Info("ItemConsumer item passed quality check", "itemID", itemID, "quality", result.Quality, "lang", result.Lang, "timeliness", result.Timeliness)

	// Correct group_id based on broadcast_type-specific rules
	correctedGroupID := resolveGroupID(itemID, raw.AuthorAgentID, result.BroadcastType, similarItems, time.Now())
	if correctedGroupID != finalGroupID {
		logger.Default().Info("ItemConsumer group_id corrected by broadcast_type rules",
			"itemID", itemID, "broadcastType", result.BroadcastType,
			"oldGroupID", finalGroupID, "newGroupID", correctedGroupID)
		finalGroupID = correctedGroupID
		// Update hash dedup cache with corrected group_id
		if err := dedup.SaveHash(ctx, mq.RDB, contentHash, finalGroupID); err != nil {
			logger.Default().Warn("ItemConsumer failed to update hash after group correction", "itemID", itemID, "err", err)
		}
	} else {
		logger.Default().Debug("ItemConsumer group_id unchanged after broadcast_type check",
			"itemID", itemID, "broadcastType", result.BroadcastType, "groupID", finalGroupID)
	}

	// Join domains array to comma-separated string
	domainsStr := ""
	if len(result.Domains) > 0 {
		domainsStr = result.Domains[0]
		for i := 1; i < len(result.Domains); i++ {
			domainsStr += "," + result.Domains[i]
		}
	}

	// Preserve publisher's "no_reply" intent over LLM analysis
	finalExpectedResponse := result.ExpectedResponse
	if publisherExpResp == "no_reply" {
		finalExpectedResponse = "no_reply"
	}

	// Update processed item with LLM results
	if !persistProcessedItem(ctx, msgID, itemID, result, domainsStr, finalExpectedResponse, finalGroupID) {
		return
	}

	// Index processed item to Elasticsearch
	esItem := &sortDal.Item{
		ID:               itemID,
		AuthorAgentID:    raw.AuthorAgentID,
		Content:          raw.RawContent,
		RawURL:           raw.RawURL,
		Summary:          result.Summary,
		Type:             result.BroadcastType,
		Domains:          result.Domains,
		Keywords:         result.Keywords,
		ExpireTime:       parseExpireTime(result.ExpireTime),
		Geo:              result.Geo,
		SourceType:       result.SourceType,
		ExpectedResponse: finalExpectedResponse,
		GroupID:          finalGroupID,
		QualityScore:     result.Quality,
		Lang:             result.Lang,
		Timeliness:       result.Timeliness,
		Embedding:        itemEmbedding,
		CreatedAt:        time.Unix(raw.CreatedAt/1000, 0),
		UpdatedAt:        time.Now(),
	}

	if err := sortDal.IndexItem(ctx, esItem); err != nil {
		logger.Default().Error("ItemConsumer failed to index item to ES", "itemID", itemID, "err", err)
		// Don't block the flow, continue to ACK
	} else {
		logger.Default().Info("ItemConsumer item indexed to ES successfully", "itemID", itemID)

		// Cache invalidation for alert items
		if result.BroadcastType == "alert" {
			go func() {
				payload := map[string]interface{}{
					"domains":  strings.Join(result.Domains, ","),
					"keywords": strings.Join(result.Keywords, ","),
				}
				if _, err := mq.Publish(context.Background(), "stream:cache:invalidate", payload); err != nil {
					logger.Default().Warn("ItemConsumer failed to publish cache invalidation", "itemID", itemID, "err", err)
				}
			}()
		}

		// Update statistics counters (fire-and-forget)
		go func() {
			bgCtx := context.Background()

			// Increment total item count
			if err := stats.IncrItemTotal(bgCtx, mq.RDB); err != nil {
				logger.Default().Warn("ItemConsumer failed to increment item total", "err", err)
			}

			// Get agent info for latest items list
			agent, err := profileDal.GetAgentByID(db.DB, raw.AuthorAgentID)
			if err != nil {
				logger.Default().Warn("ItemConsumer failed to get agent info", "itemID", itemID, "err", err)
				return
			}

			// Get agent profile for country
			profile, err := profileDal.GetAgentProfile(db.DB, raw.AuthorAgentID)
			country := ""
			if err == nil && profile != nil {
				country = profile.Country
			}

			// Parse raw_notes as JSON map
			notes := make(map[string]string)
			if raw.RawNotes != "" {
				var notesMap map[string]interface{}
				if err := json.Unmarshal([]byte(raw.RawNotes), &notesMap); err == nil {
					for k, v := range notesMap {
						if str, ok := v.(string); ok {
							notes[k] = str
						}
					}
				}
			}

			// Push to latest items list
			snapshot := &stats.ItemSnapshot{
				ID:      itemID,
				Agent:   agent.AgentName,
				Country: country,
				Type:    result.BroadcastType,
				Domains: result.Domains,
				Content: raw.RawContent,
				URL:     raw.RawURL,
				Notes:   notes,
			}

			if err := stats.PushLatestItem(bgCtx, mq.RDB, snapshot); err != nil {
				logger.Default().Warn("ItemConsumer failed to push item to latest items", "itemID", itemID, "err", err)
			} else {
				logger.Default().Debug("ItemConsumer item pushed to latest items list", "itemID", itemID)
			}
		}()
	}

	mq.Ack(ctx, itemStream, itemGroup, msgID)
}

func persistProcessedItem(ctx context.Context, msgID string, itemID int64, result *llm.ExtractResult, domainsStr, finalExpectedResponse string, finalGroupID int64) bool {
	if err := updateProcessedItem(db.DB, itemID, result.Summary, result.BroadcastType, domainsStr, result.Keywords, result.ExpireTime, result.Geo, result.SourceType, finalExpectedResponse, finalGroupID, result.Quality, result.Lang, result.Timeliness, itemDal.StatusCompleted); err != nil {
		logger.Default().Error("failed to persist processed item", "itemID", itemID, "broadcastType", result.BroadcastType, "err", err)

		if statusErr := updateProcessedItemStatus(db.DB, itemID, itemDal.StatusFailed); statusErr != nil {
			logger.Default().Error("failed to mark item as failed after persist error", "itemID", itemID, "err", statusErr)
		}

		ackItemMessage(ctx, itemStream, itemGroup, msgID)
		return false
	}

	logger.Default().Info("ItemConsumer item processed", "itemID", itemID, "broadcastType", result.BroadcastType, "domains", result.Domains, "keywords", result.Keywords, "groupID", finalGroupID, "quality", result.Quality)
	return true
}

// matchBlacklist returns the first matching keyword if any blacklist keyword is found
// in the concatenated raw content, or empty string if no match.
func matchBlacklist(keywords []string, rawContent, rawURL, rawNotes string) string {
	if len(keywords) == 0 {
		return ""
	}
	combined := strings.ToLower(rawContent + " " + rawURL + " " + rawNotes)
	for _, kw := range keywords {
		if strings.Contains(combined, strings.ToLower(kw)) {
			return kw
		}
	}
	return ""
}

// checkBlacklist loads keywords from cache and checks for matches.
func checkBlacklist(ctx context.Context, rawContent, rawURL, rawNotes string) string {
	keywords := loadBlacklistKeywords(ctx)
	return matchBlacklist(keywords, rawContent, rawURL, rawNotes)
}

// parseExpireTime parses expire time string to *time.Time
func parseExpireTime(expireTimeStr string) *time.Time {
	if expireTimeStr == "" {
		return nil
	}
	// Try multiple formats
	formats := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, expireTimeStr); err == nil {
			return &t
		}
	}
	return nil
}
