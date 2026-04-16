package dal

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

type RawItem struct {
	ItemID        int64  `gorm:"column:item_id;primaryKey"`
	AuthorAgentID int64  `gorm:"column:author_agent_id;not null"`
	RawContent    string `gorm:"column:raw_content;type:text;not null"`
	RawNotes      string `gorm:"column:raw_notes;type:text;default:''"`
	RawURL        string `gorm:"column:raw_url;type:varchar(300);default:''"`
	CreatedAt     int64  `gorm:"column:created_at;not null"`
}

func (RawItem) TableName() string { return "raw_items" }

type ProcessedItem struct {
	ItemID           int64   `gorm:"column:item_id;primaryKey"`
	Status           int16   `gorm:"column:status;type:smallint;not null;default:0"`
	Summary          string  `gorm:"column:summary;type:text;default:null"`
	BroadcastType    string  `gorm:"column:broadcast_type;type:varchar(50);not null;default:''"`
	Domains          string  `gorm:"column:domains;type:text;default:null"`
	Keywords         string  `gorm:"column:keywords;type:text;default:null"`
	ExpireTime       string  `gorm:"column:expire_time;type:varchar(100);default:null"`
	Geo              string  `gorm:"column:geo;type:varchar(200);default:null"`
	SourceType       string  `gorm:"column:source_type;type:varchar(50);default:null"`
	ExpectedResponse string  `gorm:"column:expected_response;type:text;default:null"`
	GroupID          int64   `gorm:"column:group_id;type:bigint;default:null"`
	QualityScore     float64 `gorm:"column:quality_score;type:real;default:null"`
	Lang             string  `gorm:"column:lang;type:varchar(10);default:null"`
	Timeliness       string  `gorm:"column:timeliness;type:varchar(20);default:null"`
	Suggestion       string  `gorm:"column:suggestion;type:text;default:null"`
	UpdatedAt        int64   `gorm:"column:updated_at;not null"`
}

func (ProcessedItem) TableName() string { return "processed_items" }

// Item processing status codes.
const (
	StatusPending    int16 = 0
	StatusProcessing int16 = 1
	StatusFailed     int16 = 2
	StatusCompleted  int16 = 3
	StatusDiscarded  int16 = 4
	StatusDeleted    int16 = 5
)

// type ItemStats struct {
// 	ItemID         int64 `gorm:"column:item_id;primaryKey"`
// 	AuthorAgentID  int64 `gorm:"column:author_agent_id;not null"`
// 	ConsumedCount  int64 `gorm:"column:consumed_count;not null;default:0"`
// 	ScoreNeg1Count int64 `gorm:"column:score_neg1_count;not null;default:0"`
// 	Score0Count    int64 `gorm:"column:score_0_count;not null;default:0"`
// 	Score1Count    int64 `gorm:"column:score_1_count;not null;default:0"`
// 	Score2Count    int64 `gorm:"column:score_2_count;not null;default:0"`
// 	TotalScore     int64 `gorm:"column:total_score;not null;default:0"`
// 	CreatedAt      int64 `gorm:"column:created_at;not null"`
// 	UpdatedAt      int64 `gorm:"column:updated_at;not null"`
// }

// func (ItemStats) TableName() string { return "item_stats" }

// type ItemWithStats struct {
// 	ItemID            int64  `gorm:"column:item_id"`
// 	RawContentPreview string `gorm:"column:raw_content_preview"`
// 	Summary           string `gorm:"column:summary"`
// 	BroadcastType     string `gorm:"column:broadcast_type"`
// 	ConsumedCount     int64  `gorm:"column:consumed_count"`
// 	ScoreNeg1Count    int64  `gorm:"column:score_neg1_count"`
// 	Score1Count       int64  `gorm:"column:score_1_count"`
// 	Score2Count       int64  `gorm:"column:score_2_count"`
// 	TotalScore        int64  `gorm:"column:total_score"`
// 	UpdatedAt         int64  `gorm:"column:updated_at"`
// }

// type InfluenceMetrics struct {
// 	TotalItems    int64 `gorm:"column:total_items"`
// 	TotalConsumed int64 `gorm:"column:total_consumed"`
// 	TotalScored1  int64 `gorm:"column:total_scored_1"`
// 	TotalScored2  int64 `gorm:"column:total_scored_2"`
// }

func CreateRawItem(db *gorm.DB, item *RawItem) error {
	item.CreatedAt = time.Now().UnixMilli()
	return db.Create(item).Error
}

func GetRawItemByID(db *gorm.DB, itemID int64) (*RawItem, error) {
	var item RawItem
	err := db.Where("item_id = ?", itemID).First(&item).Error
	return &item, err
}

func CreateProcessedItem(db *gorm.DB, pi *ProcessedItem) error {
	pi.UpdatedAt = time.Now().UnixMilli()
	return db.Create(pi).Error
}

func UpdateProcessedItem(db *gorm.DB, itemID int64, summary, broadcastType, domains string, keywords []string, expireTime, geo, sourceType, expectedResponse string, groupID int64, qualityScore float64, lang, timeliness, suggestion string, status int16) error {
	kw := strings.Join(keywords, ",")

	// Prepare updates map
	updates := map[string]interface{}{
		"status":            status,
		"summary":           summary,
		"broadcast_type":    broadcastType,
		"domains":           domains,
		"keywords":          kw,
		"expire_time":       expireTime,
		"geo":               geo,
		"expected_response": expectedResponse,
		"group_id":          groupID,
		"quality_score":     qualityScore,
		"lang":              lang,
		"timeliness":        timeliness,
		"suggestion":        suggestion,
		"updated_at":        time.Now().UnixMilli(),
	}

	// Handle source_type: empty string -> NULL (to satisfy DB constraint)
	if sourceType == "" {
		updates["source_type"] = nil
	} else {
		updates["source_type"] = sourceType
	}

	// Skip update if item is already deleted (terminal)
	return db.Model(&ProcessedItem{}).Where("item_id = ? AND status != ?", itemID, StatusDeleted).Updates(updates).Error
}

func UpdateSuggestion(db *gorm.DB, itemID int64, suggestion string) error {
	return db.Model(&ProcessedItem{}).
		Where("item_id = ?", itemID).
		Updates(map[string]interface{}{
			"suggestion": suggestion,
			"updated_at": time.Now().UnixMilli(),
		}).Error
}

func GetProcessedItemExpectedResponse(db *gorm.DB, itemID int64) (string, error) {
	var result struct {
		ExpectedResponse string
	}
	err := db.Table("processed_items").
		Select("COALESCE(expected_response, '') as expected_response").
		Where("item_id = ?", itemID).
		First(&result).Error
	return result.ExpectedResponse, err
}

func GetProcessedItemByID(db *gorm.DB, itemID int64) (*ProcessedItem, error) {
	var item ProcessedItem
	err := db.Where("item_id = ?", itemID).First(&item).Error
	return &item, err
}


func UpdateProcessedItemStatus(db *gorm.DB, itemID int64, status int16) error {
	// Skip update if item is already deleted (terminal)
	return db.Model(&ProcessedItem{}).Where("item_id = ? AND status != ?", itemID, StatusDeleted).Updates(map[string]interface{}{
		"status":     status,
		"updated_at": time.Now().UnixMilli(),
	}).Error
}

func BatchGetProcessedItems(db *gorm.DB, itemIDs []int64) ([]*ProcessedItem, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	var items []*ProcessedItem
	err := db.Where("item_id IN ? AND status = ?", itemIDs, StatusCompleted).Find(&items).Error
	return items, err
}

// func GetItemStatsByAuthor(db *gorm.DB, authorAgentID, lastItemID int64, limit int) ([]*ItemWithStats, error) {
// 	if limit <= 0 {
// 		limit = 20
// 	}

// 	query := db.Table("item_stats AS s").
// 		Select(`
// 			s.item_id,
// 			LEFT(COALESCE(r.raw_content, ''), 200) AS raw_content_preview,
// 			COALESCE(p.summary, '') AS summary,
// 			COALESCE(p.broadcast_type, '') AS broadcast_type,
// 			s.consumed_count,
// 			s.score_neg1_count,
// 			s.score_1_count,
// 			s.score_2_count,
// 			s.total_score,
// 			COALESCE(p.updated_at, s.updated_at) AS updated_at
// 		`).
// 		Joins("LEFT JOIN raw_items r ON r.item_id = s.item_id").
// 		Joins("LEFT JOIN processed_items p ON p.item_id = s.item_id").
// 		Where("s.author_agent_id = ?", authorAgentID)

// 	if lastItemID > 0 {
// 		query = query.Where("s.item_id < ?", lastItemID)
// 	}

// 	var items []*ItemWithStats
// 	err := query.Order("s.item_id DESC").Limit(limit).Scan(&items).Error
// 	return items, err
// }

// func GetAgentInfluenceMetrics(db *gorm.DB, authorAgentID int64) (*InfluenceMetrics, error) {
// 	var metrics InfluenceMetrics
// 	err := db.Table("item_stats").
// 		Select(`
// 			COUNT(*) AS total_items,
// 			COALESCE(SUM(consumed_count), 0) AS total_consumed,
// 			COALESCE(SUM(score_1_count), 0) AS total_scored_1,
// 			COALESCE(SUM(score_2_count), 0) AS total_scored_2
// 		`).
// 		Where("author_agent_id = ?", authorAgentID).
// 		Scan(&metrics).Error
// 	if err != nil {
// 		return nil, err
// 	}
// 	return &metrics, nil
// }

func GetLatestItems(db *gorm.DB, lastItemID int64, limit int) ([]*ProcessedItem, error) {
	if limit <= 0 {
		limit = 20
	}
	var items []*ProcessedItem
	tx := db.Where("status = ?", StatusCompleted)
	if lastItemID > 0 {
		tx = tx.Where("item_id > ?", lastItemID)
	}
	err := tx.Order("item_id ASC").Limit(limit).Find(&items).Error
	return items, err
}

// ItemWithURL combines ProcessedItem with URL from RawItem
type ItemWithURL struct {
	ProcessedItem
	RawURL     string
	RawContent string
}

func GetItemByID(db *gorm.DB, itemID int64) (*ItemWithURL, error) {
	var result ItemWithURL
	err := db.Table("processed_items").
		Select("processed_items.*, raw_items.raw_url, raw_items.raw_content").
		Joins("LEFT JOIN raw_items ON processed_items.item_id = raw_items.item_id").
		Where("processed_items.item_id = ? AND processed_items.status = ?", itemID, StatusCompleted).
		First(&result).Error
	return &result, err
}

func BatchGetItemsWithURL(db *gorm.DB, itemIDs []int64) ([]*ItemWithURL, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	var items []*ItemWithURL
	err := db.Table("processed_items").
		Select("processed_items.*, raw_items.raw_url").
		Joins("LEFT JOIN raw_items ON processed_items.item_id = raw_items.item_id").
		Where("processed_items.item_id IN ? AND processed_items.status = ?", itemIDs, StatusCompleted).
		Find(&items).Error
	return items, err
}

// GetItemsSince fetches items updated since the given timestamp
func GetItemsSince(db *gorm.DB, sinceUpdatedAt int64, limit int) ([]*ItemWithURL, error) {
	if limit <= 0 {
		limit = 20
	}
	var items []*ItemWithURL
	tx := db.Table("processed_items").
		Select("processed_items.*, raw_items.raw_url").
		Joins("LEFT JOIN raw_items ON processed_items.item_id = raw_items.item_id").
		Where("processed_items.status = ?", StatusCompleted)

	if sinceUpdatedAt > 0 {
		tx = tx.Where("processed_items.updated_at > ?", sinceUpdatedAt)
	}
	// FeedUpdates uses cursor semantics: updated_at > since_updated_at.
	// Query in ascending order so the returned next_cursor can advance page-by-page.
	err := tx.Order("processed_items.updated_at ASC, processed_items.item_id ASC").Limit(limit).Find(&items).Error
	return items, err
}

// ItemStats represents item statistics
type ItemStats struct {
	ItemID         int64 `gorm:"primaryKey;column:item_id"`
	AuthorAgentID  int64 `gorm:"column:author_agent_id;not null"`
	ConsumedCount  int64 `gorm:"column:consumed_count;default:0"`
	ScoreNeg1Count int64 `gorm:"column:score_neg1_count;default:0"`
	Score0Count    int64 `gorm:"column:score_0_count;default:0"`
	Score1Count    int64 `gorm:"column:score_1_count;default:0"`
	Score2Count    int64 `gorm:"column:score_2_count;default:0"`
	TotalScore     int64 `gorm:"column:total_score;default:0"`
	CreatedAt      int64 `gorm:"column:created_at;not null"`
	UpdatedAt      int64 `gorm:"column:updated_at;not null"`
}

func (ItemStats) TableName() string { return "item_stats" }

// CreateItemStats creates a new item stats record
func CreateItemStats(db *gorm.DB, itemID, authorAgentID int64) error {
	now := time.Now().UnixMilli()
	stats := &ItemStats{
		ItemID:        itemID,
		AuthorAgentID: authorAgentID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	return db.Create(stats).Error
}

// IncrementConsumedCount increments the consumed count for an item
func IncrementConsumedCount(db *gorm.DB, itemID int64) error {
	return db.Model(&ItemStats{}).
		Where("item_id = ?", itemID).
		Updates(map[string]interface{}{
			"consumed_count": gorm.Expr("consumed_count + 1"),
			"updated_at":     time.Now().UnixMilli(),
		}).Error
}

// IncrementItemScore increments the score count for an item
func IncrementItemScore(db *gorm.DB, itemID int64, score int) error {
	now := time.Now().UnixMilli()
	var scoreField string
	var scoreWeight int64

	switch score {
	case -1:
		scoreField = "score_neg1_count"
		scoreWeight = 0 // negative scores don't contribute to total
	case 0:
		scoreField = "score_0_count"
		scoreWeight = 0
	case 1:
		scoreField = "score_1_count"
		scoreWeight = 1
	case 2:
		scoreField = "score_2_count"
		scoreWeight = 2
	default:
		return nil // invalid score, skip
	}

	return db.Model(&ItemStats{}).
		Where("item_id = ?", itemID).
		Updates(map[string]interface{}{
			scoreField:    gorm.Expr(scoreField + " + 1"),
			"total_score": gorm.Expr("total_score + ?", scoreWeight),
			"updated_at":  now,
		}).Error
}

// GetItemStatsByID retrieves stats for a single item
func GetItemStatsByID(db *gorm.DB, itemID int64) (*ItemStats, error) {
	var stats ItemStats
	err := db.Where("item_id = ?", itemID).First(&stats).Error
	if err != nil {
		return nil, err
	}
	return &stats, nil
}

// ItemWithStats combines item data with statistics
type ItemWithStats struct {
	ItemID            int64
	RawContentPreview string
	Summary           string
	BroadcastType     string
	ConsumedCount     int64
	ScoreNeg1Count    int64
	Score1Count       int64
	Score2Count       int64
	TotalScore        int64
	UpdatedAt         int64
}

// GetItemStatsByAuthor retrieves items with stats for a specific author
// Optimized version: avoid JOINs by querying tables separately
func GetItemStatsByAuthor(db *gorm.DB, authorAgentID, lastItemID int64, limit int) ([]*ItemWithStats, error) {
	// Step 1: Query item_stats table, excluding deleted items
	var stats []ItemStats
	query := db.Table("item_stats").
		Joins("INNER JOIN processed_items ON item_stats.item_id = processed_items.item_id").
		Where("item_stats.author_agent_id = ?", authorAgentID).
		Where("processed_items.status != ?", StatusDeleted)
	if lastItemID > 0 {
		query = query.Where("item_stats.item_id < ?", lastItemID)
	}
	err := query.
		Select("item_stats.*").
		Order("item_stats.updated_at DESC, item_stats.item_id DESC").
		Limit(limit).
		Find(&stats).Error
	if err != nil {
		return nil, err
	}

	if len(stats) == 0 {
		return []*ItemWithStats{}, nil
	}

	// Step 2: Collect item IDs
	itemIDs := make([]int64, len(stats))
	statsMap := make(map[int64]*ItemStats)
	for i, s := range stats {
		itemIDs[i] = s.ItemID
		statsMap[s.ItemID] = &stats[i]
	}

	// Step 3: Batch query raw_items for content preview
	var rawItems []struct {
		ItemID     int64
		RawContent string
	}
	err = db.Table("raw_items").
		Select("item_id, SUBSTRING(raw_content, 1, 200) as raw_content").
		Where("item_id IN ?", itemIDs).
		Find(&rawItems).Error
	if err != nil {
		return nil, err
	}
	rawItemsMap := make(map[int64]string)
	for _, ri := range rawItems {
		rawItemsMap[ri.ItemID] = ri.RawContent
	}

	// Step 4: Batch query processed_items for summary and broadcast_type
	var processedItems []struct {
		ItemID        int64
		Summary       string
		BroadcastType string
	}
	err = db.Table("processed_items").
		Select("item_id, summary, broadcast_type").
		Where("item_id IN ?", itemIDs).
		Find(&processedItems).Error
	if err != nil {
		return nil, err
	}
	processedItemsMap := make(map[int64]struct {
		Summary       string
		BroadcastType string
	})
	for _, pi := range processedItems {
		processedItemsMap[pi.ItemID] = struct {
			Summary       string
			BroadcastType string
		}{Summary: pi.Summary, BroadcastType: pi.BroadcastType}
	}

	// Step 5: Assemble results in original order
	results := make([]*ItemWithStats, 0, len(stats))
	for _, s := range stats {
		result := &ItemWithStats{
			ItemID:            s.ItemID,
			RawContentPreview: rawItemsMap[s.ItemID],
			ConsumedCount:     s.ConsumedCount,
			ScoreNeg1Count:    s.ScoreNeg1Count,
			Score1Count:       s.Score1Count,
			Score2Count:       s.Score2Count,
			TotalScore:        s.TotalScore,
			UpdatedAt:         s.UpdatedAt,
		}
		if pi, ok := processedItemsMap[s.ItemID]; ok {
			result.Summary = pi.Summary
			result.BroadcastType = pi.BroadcastType
		}
		results = append(results, result)
	}

	return results, nil
}

// InfluenceMetrics represents aggregated influence metrics for an agent
type InfluenceMetrics struct {
	TotalItems    int64
	TotalConsumed int64
	TotalScored1  int64
	TotalScored2  int64
}

// GetAgentInfluenceMetrics retrieves aggregated influence metrics for an agent
// Optimized: uses indexed query on author_agent_id
func GetAgentInfluenceMetrics(db *gorm.DB, agentID int64) (*InfluenceMetrics, error) {
	var result InfluenceMetrics

	// Use indexed query - author_agent_id has index
	err := db.Model(&ItemStats{}).
		Select(`
			COUNT(*) as total_items,
			COALESCE(SUM(consumed_count), 0) as total_consumed,
			COALESCE(SUM(score_1_count), 0) as total_scored1,
			COALESCE(SUM(score_2_count), 0) as total_scored2
		`).
		Where("author_agent_id = ?", agentID).
		Scan(&result).Error

	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GetItemsByGroupID retrieves items by group_id
func GetItemsByGroupID(db *gorm.DB, groupID int64) ([]*ProcessedItem, error) {
	var items []*ProcessedItem
	err := db.Where("group_id = ?", groupID).Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// BatchGetRawItemAuthors retrieves author_agent_id for multiple items
func BatchGetRawItemAuthors(db *gorm.DB, itemIDs []int64) (map[int64]int64, error) {
	if len(itemIDs) == 0 {
		return make(map[int64]int64), nil
	}

	var results []struct {
		ItemID        int64
		AuthorAgentID int64
	}

	err := db.Table("raw_items").
		Select("item_id, author_agent_id").
		Where("item_id IN ?", itemIDs).
		Find(&results).Error

	if err != nil {
		return nil, err
	}

	authorMap := make(map[int64]int64, len(results))
	for _, r := range results {
		authorMap[r.ItemID] = r.AuthorAgentID
	}

	return authorMap, nil
}
