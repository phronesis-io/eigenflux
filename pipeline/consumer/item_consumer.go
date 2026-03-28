package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/dedup"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/stats"
	itemDal "eigenflux_server/rpc/item/dal"
	profileDal "eigenflux_server/rpc/profile/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
)

const (
	itemStream = "stream:item:publish"
	itemGroup  = "cg:item:publish"

	simThreshold = 0.70
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

func NewItemConsumer(cfg *config.Config) *ItemConsumer {
	return &ItemConsumer{
		llmClient:        llm.NewClient(cfg),
		embeddingClient:  embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions),
		qualityThreshold: cfg.QualityThreshold,
		maxWorkers:       cfg.ItemConsumerWorkers,
	}
}

func (c *ItemConsumer) Start(ctx context.Context) {
	log.Printf("[ItemConsumer] starting with %d workers, quality threshold: %.2f", c.maxWorkers, c.qualityThreshold)

	if err := mq.EnsureConsumerGroup(ctx, itemStream, itemGroup); err != nil {
		log.Fatalf("[ItemConsumer] failed to create consumer group: %v", err)
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
			log.Printf("[ItemConsumer] worker %d started", workerID)
			for task := range msgChan {
				c.processMessage(ctx, task.id, task.values)
			}
			log.Printf("[ItemConsumer] worker %d stopped", workerID)
		}(i)
	}

	// Main loop: fetch messages and distribute to workers
	go func() {
		for {
			select {
			case <-ctx.Done():
				log.Println("[ItemConsumer] context cancelled, closing message channel")
				close(msgChan)
				return
			default:
			}

			msgs, err := mq.Consume(ctx, itemStream, itemGroup, "item-worker-1", 10)
			if err != nil {
				log.Printf("[ItemConsumer] consume error: %v", err)
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
					log.Println("[ItemConsumer] context cancelled while sending message")
					close(msgChan)
					return
				}
			}
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	log.Println("[ItemConsumer] shutting down, waiting for workers to finish...")
	wg.Wait()
	log.Println("[ItemConsumer] all workers stopped")
}

func (c *ItemConsumer) processMessage(ctx context.Context, msgID string, values map[string]interface{}) {
	itemIDStr, ok := values["item_id"].(string)
	if !ok {
		log.Printf("[ItemConsumer] invalid message: missing item_id")
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
	if err != nil {
		log.Printf("[ItemConsumer] invalid item_id: %s", itemIDStr)
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	log.Printf("[ItemConsumer] processing item_id=%d", itemID)

	// Preserve publisher's expected_response if set to "no_reply"
	publisherExpResp, _ := itemDal.GetProcessedItemExpectedResponse(db.DB, itemID)

	// Set status to processing
	itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusProcessing)

	// Get raw item
	raw, err := itemDal.GetRawItemByID(db.DB, itemID)
	if err != nil {
		log.Printf("[ItemConsumer] raw item not found: %d, err: %v", itemID, err)
		itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusFailed)
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	// Check blacklist keywords before LLM processing
	if matched := checkBlacklist(ctx, raw.RawContent, raw.RawURL, raw.RawNotes); matched != "" {
		log.Printf("[ItemConsumer] item %d discarded by blacklist keyword: %q", itemID, matched)
		if err := itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusDiscarded); err != nil {
			log.Printf("[ItemConsumer] failed to update discard status for item %d: %v", itemID, err)
			return
		}
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	// Call LLM to process item with retries
	var result *llm.ExtractResult
	for attempt := 1; attempt <= maxRetries; attempt++ {
		result, err = c.llmClient.ProcessItem(ctx, raw.RawContent, raw.RawNotes)
		if err == nil {
			break
		}
		log.Printf("[ItemConsumer] LLM attempt %d/%d failed for item %d: %v", attempt, maxRetries, itemID, err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}

	if err != nil {
		log.Printf("[ItemConsumer] all retries failed for item %d: %v", itemID, err)
		itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusFailed)
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	// Check discard flag (skip in non-dev environments for testing)
	if result.Discard {
		log.Printf("[ItemConsumer] item %d discarded by LLM: %s", itemID, result.DiscardReason)
		if err := itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusDiscarded); err != nil {
			log.Printf("[ItemConsumer] failed to update discard status for item %d: %v", itemID, err)
			return
		}
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	// Check quality threshold
	if result.Quality < c.qualityThreshold {
		log.Printf("[ItemConsumer] item %d quality score %.2f below threshold %.2f, discarding", itemID, result.Quality, c.qualityThreshold)
		if err := itemDal.UpdateProcessedItemStatus(db.DB, itemID, itemDal.StatusDiscarded); err != nil {
			log.Printf("[ItemConsumer] failed to update discard status for item %d: %v", itemID, err)
			return
		}
		mq.Ack(ctx, itemStream, itemGroup, msgID)
		return
	}

	log.Printf("[ItemConsumer] item %d passed quality check: score=%.2f, lang=%s, timeliness=%s", itemID, result.Quality, result.Lang, result.Timeliness)

	// Join domains array to comma-separated string
	domainsStr := ""
	if len(result.Domains) > 0 {
		domainsStr = result.Domains[0]
		for i := 1; i < len(result.Domains); i++ {
			domainsStr += "," + result.Domains[i]
		}
	}

	// Phase 1: Hash-based deduplication (fast path for exact duplicates)
	var itemEmbedding []float32
	var finalGroupID int64
	skipEmbedding := false

	contentHash := dedup.ComputeContentHash(raw.RawContent)
	log.Printf("[ItemConsumer] item %d content hash: %s", itemID, contentHash)

	// Check if hash exists in Redis (best-effort, don't fail if Redis is down)
	if hashExists, existingGroupID, err := dedup.CheckHashExists(ctx, mq.RDB, contentHash); err == nil && hashExists {
		// Exact duplicate found, reuse existing group_id
		finalGroupID = existingGroupID
		skipEmbedding = true
		log.Printf("[ItemConsumer] item %d is exact duplicate (hash match), reusing group_id: %d", itemID, finalGroupID)
	} else if err != nil {
		log.Printf("[ItemConsumer] Redis hash check failed for item %d: %v, falling back to vector search", itemID, err)
	}

	// generate embedding
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
		log.Printf("[ItemConsumer] embedding attempt %d/%d failed for item %d: %v", embAttempt, maxRetries, itemID, embErr)
		if embAttempt < maxRetries {
			time.Sleep(time.Duration(embAttempt) * time.Second)
		}
	}

	// Phase 2: Vector-based deduplication (for non-exact duplicates)
	if !skipEmbedding {
		if itemEmbedding == nil {
			log.Printf("[ItemConsumer] all embedding attempts failed for item %d, using item_id as group_id", itemID)
			finalGroupID = itemID
		} else {
			// Search for similar items
			similarItems, err := sortDal.SearchSimilarItems(ctx, itemEmbedding, simThreshold, 5)
			if err != nil {
				log.Printf("[ItemConsumer] similarity search failed for item %d: %v", itemID, err)
				finalGroupID = itemID
			} else if len(similarItems) > 0 {
				// Found similar item, use its group_id
				finalGroupID = similarItems[0].GroupID
				log.Printf("[ItemConsumer] item %d matched to group %d (similarity with item %d)", itemID, finalGroupID, similarItems[0].ID)
			} else {
				// No similar item found, use own item_id as group_id
				finalGroupID = itemID
				log.Printf("[ItemConsumer] item %d is unique, creating new group %d", itemID, finalGroupID)
			}
		}

		// Save hash mapping for future deduplication (best-effort)
		if err := dedup.SaveHash(ctx, mq.RDB, contentHash, finalGroupID); err != nil {
			log.Printf("[ItemConsumer] failed to save hash for item %d: %v", itemID, err)
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
		log.Printf("[ItemConsumer] failed to index item %d to ES: %v", itemID, err)
		// Don't block the flow, continue to ACK
	} else {
		log.Printf("[ItemConsumer] item %d indexed to ES successfully", itemID)

		// Update statistics counters (fire-and-forget)
		go func() {
			bgCtx := context.Background()

			// Increment total item count
			if err := stats.IncrItemTotal(bgCtx, mq.RDB); err != nil {
				log.Printf("[ItemConsumer] failed to increment item total: %v", err)
			}

			// Get agent info for latest items list
			agent, err := profileDal.GetAgentByID(db.DB, raw.AuthorAgentID)
			if err != nil {
				log.Printf("[ItemConsumer] failed to get agent info for item %d: %v", itemID, err)
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
				log.Printf("[ItemConsumer] failed to push item %d to latest items: %v", itemID, err)
			} else {
				log.Printf("[ItemConsumer] item %d pushed to latest items list", itemID)
			}
		}()
	}

	mq.Ack(ctx, itemStream, itemGroup, msgID)
}

func persistProcessedItem(ctx context.Context, msgID string, itemID int64, result *llm.ExtractResult, domainsStr, finalExpectedResponse string, finalGroupID int64) bool {
	if err := updateProcessedItem(db.DB, itemID, result.Summary, result.BroadcastType, domainsStr, result.Keywords, result.ExpireTime, result.Geo, result.SourceType, finalExpectedResponse, finalGroupID, result.Quality, result.Lang, result.Timeliness, itemDal.StatusCompleted); err != nil {
		log.Printf("[ItemConsumer] failed to persist processed item %d: broadcast_type=%s, err=%v", itemID, result.BroadcastType, err)

		if statusErr := updateProcessedItemStatus(db.DB, itemID, itemDal.StatusFailed); statusErr != nil {
			log.Printf("[ItemConsumer] failed to mark item %d as failed after persist error: %v", itemID, statusErr)
		}

		ackItemMessage(ctx, itemStream, itemGroup, msgID)
		return false
	}

	log.Printf("[ItemConsumer] item %d processed: broadcast_type=%s, domains=%v, keywords=%v, group_id=%d, quality=%.2f", itemID, result.BroadcastType, result.Domains, result.Keywords, finalGroupID, result.Quality)
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
