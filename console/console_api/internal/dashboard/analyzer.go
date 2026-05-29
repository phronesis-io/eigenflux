package dashboard

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// SnapshotData is the top-level structure stored as JSON in the data column.
type SnapshotData struct {
	Summary         Summary         `json:"summary"`
	KeywordAnalysis KeywordAnalysis `json:"keyword_analysis"`
	DomainAnalysis  DomainAnalysis  `json:"domain_analysis"`
	Engagement      Engagement      `json:"engagement"`
}

type Summary struct {
	TotalItems      int64   `json:"total_items"`
	ActiveItems     int64   `json:"active_items"`
	TotalUsers      int64   `json:"total_users"`
	AvgQualityScore float64 `json:"avg_quality_score"`
}

type KeywordCount struct {
	Keyword string `json:"keyword"`
	Count   int    `json:"count"`
}

type OverlapEntry struct {
	Keyword   string `json:"keyword"`
	ItemCount int    `json:"item_count"`
	UserCount int    `json:"user_count"`
}

type KeywordAnalysis struct {
	ItemKeywords []KeywordCount `json:"item_keywords"`
	UserKeywords []KeywordCount `json:"user_keywords"`
	Overlap      []OverlapEntry `json:"overlap"`
	SupplyOnly   []KeywordCount `json:"supply_only"`
	DemandOnly   []KeywordCount `json:"demand_only"`
}

type DomainCount struct {
	Domain      string  `json:"domain"`
	Count       int     `json:"count"`
	AvgConsumed float64 `json:"avg_consumed"`
}

type DomainAnalysis struct {
	BroadcastTypeDistribution map[string]int `json:"broadcast_type_distribution"`
	TopDomains                []DomainCount  `json:"top_domains"`
}

type QualityBucket struct {
	Range string `json:"range"`
	Count int    `json:"count"`
}

type KeywordRate struct {
	Keyword string  `json:"keyword"`
	Rate    float64 `json:"rate"`
}

type TopItem struct {
	ItemID        int64   `json:"item_id"`
	Keywords      string  `json:"keywords"`
	ConsumedCount int64   `json:"consumed_count"`
	TotalScore    int64   `json:"total_score"`
	QualityScore  float64 `json:"quality_score"`
}

type Engagement struct {
	ConsumedRateByKeyword []KeywordRate   `json:"consumed_rate_by_keyword"`
	QualityDistribution   []QualityBucket `json:"quality_distribution"`
	Top50Items            []TopItem       `json:"top50_items"`
}

// ComputeSnapshot runs all analysis queries and returns the snapshot JSON.
// Item-level analysis is scoped to the last 7 days to keep query sizes manageable.
func ComputeSnapshot(db *gorm.DB) (json.RawMessage, error) {
	sinceMs := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()

	summary, err := computeSummary(db)
	if err != nil {
		return nil, err
	}

	itemKeywordFreq, err := queryItemKeywords(db, sinceMs)
	if err != nil {
		return nil, err
	}

	userKeywordFreq, err := queryUserKeywords(db)
	if err != nil {
		return nil, err
	}

	keywordAnalysis := computeKeywordAnalysis(itemKeywordFreq, userKeywordFreq)

	domainAnalysis, err := computeDomainAnalysis(db, sinceMs)
	if err != nil {
		return nil, err
	}

	engagement, err := computeEngagement(db, sinceMs)
	if err != nil {
		return nil, err
	}

	data := SnapshotData{
		Summary:         summary,
		KeywordAnalysis: keywordAnalysis,
		DomainAnalysis:  domainAnalysis,
		Engagement:      engagement,
	}

	return json.Marshal(data)
}

func computeSummary(db *gorm.DB) (Summary, error) {
	var s Summary

	if err := db.Table("processed_items").Count(&s.TotalItems).Error; err != nil {
		return s, err
	}

	if err := db.Table("processed_items").Where("status = 3").Count(&s.ActiveItems).Error; err != nil {
		return s, err
	}

	if err := db.Table("agent_profiles").Where("status = 3").Count(&s.TotalUsers).Error; err != nil {
		return s, err
	}

	var avgQ *float64
	if err := db.Table("processed_items").Where("status = 3 AND quality_score IS NOT NULL").
		Select("AVG(quality_score)").Scan(&avgQ).Error; err != nil {
		return s, err
	}
	if avgQ != nil {
		s.AvgQualityScore = math.Round(*avgQ*100) / 100
	}

	return s, nil
}

func queryItemKeywords(db *gorm.DB, sinceMs int64) (map[string]int, error) {
	var rows []struct {
		Keywords string `gorm:"column:keywords"`
	}
	if err := db.Table("processed_items").
		Select("keywords").
		Where("status = 3 AND keywords != '' AND updated_at >= ?", sinceMs).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	freq := make(map[string]int)
	for _, r := range rows {
		for _, kw := range strings.Split(r.Keywords, ",") {
			kw = strings.TrimSpace(strings.ToLower(kw))
			if kw != "" {
				freq[kw]++
			}
		}
	}
	return freq, nil
}

func queryUserKeywords(db *gorm.DB) (map[string]int, error) {
	var rows []struct {
		Keywords string `gorm:"column:keywords"`
	}
	if err := db.Table("agent_profiles").
		Select("keywords").
		Where("status = 3 AND keywords != ''").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	freq := make(map[string]int)
	for _, r := range rows {
		for _, kw := range strings.Split(r.Keywords, ",") {
			kw = strings.TrimSpace(strings.ToLower(kw))
			if kw != "" {
				freq[kw]++
			}
		}
	}
	return freq, nil
}

func computeKeywordAnalysis(itemFreq, userFreq map[string]int) KeywordAnalysis {
	var ka KeywordAnalysis

	ka.ItemKeywords = topN(itemFreq, 50)
	ka.UserKeywords = topN(userFreq, 50)

	for kw, itemCount := range itemFreq {
		if userCount, ok := userFreq[kw]; ok {
			ka.Overlap = append(ka.Overlap, OverlapEntry{
				Keyword:   kw,
				ItemCount: itemCount,
				UserCount: userCount,
			})
		} else {
			ka.SupplyOnly = append(ka.SupplyOnly, KeywordCount{Keyword: kw, Count: itemCount})
		}
	}
	for kw, userCount := range userFreq {
		if _, ok := itemFreq[kw]; !ok {
			ka.DemandOnly = append(ka.DemandOnly, KeywordCount{Keyword: kw, Count: userCount})
		}
	}

	sort.Slice(ka.Overlap, func(i, j int) bool {
		return ka.Overlap[i].ItemCount+ka.Overlap[i].UserCount > ka.Overlap[j].ItemCount+ka.Overlap[j].UserCount
	})
	if len(ka.Overlap) > 50 {
		ka.Overlap = ka.Overlap[:50]
	}
	ka.SupplyOnly = topNFromSlice(ka.SupplyOnly, 50)
	ka.DemandOnly = topNFromSlice(ka.DemandOnly, 50)

	return ka
}

func computeDomainAnalysis(db *gorm.DB, sinceMs int64) (DomainAnalysis, error) {
	var da DomainAnalysis
	da.BroadcastTypeDistribution = make(map[string]int)

	var btRows []struct {
		BroadcastType string `gorm:"column:broadcast_type"`
		Count         int    `gorm:"column:count"`
	}
	if err := db.Table("processed_items").
		Select("broadcast_type, COUNT(*) as count").
		Where("status = 3 AND updated_at >= ?", sinceMs).
		Group("broadcast_type").
		Find(&btRows).Error; err != nil {
		return da, err
	}
	for _, r := range btRows {
		bt := r.BroadcastType
		if bt == "" {
			bt = "unknown"
		}
		da.BroadcastTypeDistribution[bt] = r.Count
	}

	var domainRows []struct {
		Domains string `gorm:"column:domains"`
		ItemID  int64  `gorm:"column:item_id"`
	}
	if err := db.Table("processed_items").
		Select("domains, item_id").
		Where("status = 3 AND domains != '' AND updated_at >= ?", sinceMs).
		Find(&domainRows).Error; err != nil {
		return da, err
	}

	itemIDs := make([]int64, 0, len(domainRows))
	for _, r := range domainRows {
		itemIDs = append(itemIDs, r.ItemID)
	}

	consumedMap := make(map[int64]int64)
	if len(itemIDs) > 0 {
		const batchSize = 50000
		for start := 0; start < len(itemIDs); start += batchSize {
			end := start + batchSize
			if end > len(itemIDs) {
				end = len(itemIDs)
			}
			var statsRows []struct {
				ItemID        int64 `gorm:"column:item_id"`
				ConsumedCount int64 `gorm:"column:consumed_count"`
			}
			if err := db.Table("item_stats").
				Select("item_id, consumed_count").
				Where("item_id IN ?", itemIDs[start:end]).
				Find(&statsRows).Error; err != nil {
				return da, err
			}
			for _, s := range statsRows {
				consumedMap[s.ItemID] = s.ConsumedCount
			}
		}
	}

	type domainAgg struct {
		count         int
		totalConsumed int64
	}
	domainAggMap := make(map[string]*domainAgg)
	for _, r := range domainRows {
		for _, d := range strings.Split(r.Domains, ",") {
			d = strings.TrimSpace(strings.ToLower(d))
			if d == "" {
				continue
			}
			agg, ok := domainAggMap[d]
			if !ok {
				agg = &domainAgg{}
				domainAggMap[d] = agg
			}
			agg.count++
			agg.totalConsumed += consumedMap[r.ItemID]
		}
	}

	domains := make([]DomainCount, 0, len(domainAggMap))
	for d, agg := range domainAggMap {
		avgConsumed := float64(0)
		if agg.count > 0 {
			avgConsumed = math.Round(float64(agg.totalConsumed)/float64(agg.count)*100) / 100
		}
		domains = append(domains, DomainCount{Domain: d, Count: agg.count, AvgConsumed: avgConsumed})
	}
	sort.Slice(domains, func(i, j int) bool { return domains[i].Count > domains[j].Count })
	if len(domains) > 30 {
		domains = domains[:30]
	}
	da.TopDomains = domains

	return da, nil
}

func computeEngagement(db *gorm.DB, sinceMs int64) (Engagement, error) {
	var e Engagement

	var qualityRows []struct {
		QualityScore *float64 `gorm:"column:quality_score"`
	}
	if err := db.Table("processed_items").
		Select("quality_score").
		Where("status = 3 AND updated_at >= ?", sinceMs).
		Find(&qualityRows).Error; err != nil {
		return e, err
	}

	buckets := map[string]int{
		"0-0.2": 0, "0.2-0.4": 0, "0.4-0.6": 0, "0.6-0.8": 0, "0.8-1.0": 0, "null": 0,
	}
	for _, r := range qualityRows {
		if r.QualityScore == nil {
			buckets["null"]++
			continue
		}
		q := *r.QualityScore
		switch {
		case q < 0.2:
			buckets["0-0.2"]++
		case q < 0.4:
			buckets["0.2-0.4"]++
		case q < 0.6:
			buckets["0.4-0.6"]++
		case q < 0.8:
			buckets["0.6-0.8"]++
		default:
			buckets["0.8-1.0"]++
		}
	}
	for _, r := range []string{"0-0.2", "0.2-0.4", "0.4-0.6", "0.6-0.8", "0.8-1.0", "null"} {
		e.QualityDistribution = append(e.QualityDistribution, QualityBucket{Range: r, Count: buckets[r]})
	}

	var topItems []struct {
		ItemID        int64   `gorm:"column:item_id"`
		Keywords      string  `gorm:"column:keywords"`
		QualityScore  float64 `gorm:"column:quality_score"`
		ConsumedCount int64   `gorm:"column:consumed_count"`
		TotalScore    int64   `gorm:"column:total_score"`
	}
	if err := db.Table("processed_items").
		Select("processed_items.item_id, processed_items.keywords, COALESCE(processed_items.quality_score, 0) as quality_score, COALESCE(item_stats.consumed_count, 0) as consumed_count, COALESCE(item_stats.total_score, 0) as total_score").
		Joins("LEFT JOIN item_stats ON processed_items.item_id = item_stats.item_id").
		Where("processed_items.status = 3 AND processed_items.updated_at >= ?", sinceMs).
		Order("COALESCE(item_stats.consumed_count, 0) + COALESCE(item_stats.total_score, 0) DESC").
		Limit(50).
		Find(&topItems).Error; err != nil {
		return e, err
	}
	for _, t := range topItems {
		e.Top50Items = append(e.Top50Items, TopItem{
			ItemID:        t.ItemID,
			Keywords:      t.Keywords,
			ConsumedCount: t.ConsumedCount,
			TotalScore:    t.TotalScore,
			QualityScore:  t.QualityScore,
		})
	}

	var allItemStats []struct {
		ItemID        int64  `gorm:"column:item_id"`
		Keywords      string `gorm:"column:keywords"`
		ConsumedCount int64  `gorm:"column:consumed_count"`
	}
	if err := db.Table("processed_items").
		Select("processed_items.item_id, processed_items.keywords, COALESCE(item_stats.consumed_count, 0) as consumed_count").
		Joins("LEFT JOIN item_stats ON processed_items.item_id = item_stats.item_id").
		Where("processed_items.status = 3 AND processed_items.keywords != '' AND processed_items.updated_at >= ?", sinceMs).
		Find(&allItemStats).Error; err != nil {
		return e, err
	}

	type kwAgg struct {
		totalConsumed int64
		count         int
	}
	kwConsumed := make(map[string]*kwAgg)
	for _, row := range allItemStats {
		for _, kw := range strings.Split(row.Keywords, ",") {
			kw = strings.TrimSpace(strings.ToLower(kw))
			if kw == "" {
				continue
			}
			agg, ok := kwConsumed[kw]
			if !ok {
				agg = &kwAgg{}
				kwConsumed[kw] = agg
			}
			agg.totalConsumed += row.ConsumedCount
			agg.count++
		}
	}

	kwRates := make([]KeywordRate, 0, len(kwConsumed))
	for kw, agg := range kwConsumed {
		if agg.count > 0 {
			rate := math.Round(float64(agg.totalConsumed)/float64(agg.count)*100) / 100
			kwRates = append(kwRates, KeywordRate{Keyword: kw, Rate: rate})
		}
	}
	sort.Slice(kwRates, func(i, j int) bool { return kwRates[i].Rate > kwRates[j].Rate })
	if len(kwRates) > 20 {
		kwRates = kwRates[:20]
	}
	e.ConsumedRateByKeyword = kwRates

	return e, nil
}

func topN(freq map[string]int, n int) []KeywordCount {
	items := make([]KeywordCount, 0, len(freq))
	for k, v := range freq {
		items = append(items, KeywordCount{Keyword: k, Count: v})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Count > items[j].Count })
	if len(items) > n {
		items = items[:n]
	}
	return items
}

func topNFromSlice(items []KeywordCount, n int) []KeywordCount {
	sort.Slice(items, func(i, j int) bool { return items[i].Count > items[j].Count })
	if len(items) > n {
		items = items[:n]
	}
	return items
}
