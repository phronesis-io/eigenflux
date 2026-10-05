package featureindex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const BroadcastGeneration = "v1"

type BroadcastDocument struct {
	ItemID        int64             `json:"item_id"`
	Version       int64             `json:"version"`
	AuthorID      int64             `json:"author_id"`
	Active        bool              `json:"active"`
	ContentHash   string            `json:"content_hash"`
	URL           string            `json:"url"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
	GroupID       int64             `json:"group_id"`
	BroadcastType string            `json:"broadcast_type"`
	SourceType    string            `json:"source_type"`
	Lang          string            `json:"lang"`
	ExpireTime    string            `json:"expire_time"`
	QualityScore  float64           `json:"quality_score"`
	Slots         searchindex.Slots `json:"retrieval_slots"`
}

type BroadcastIndex struct {
	DB    *gorm.DB
	Redis *redis.Client
}

func (s BroadcastIndex) View() string       { return Broadcast }
func (s BroadcastIndex) Generation() string { return BroadcastGeneration }
func (s BroadcastIndex) Forward() Forward {
	return Forward{Redis: s.Redis, Namespace: "broadcast:" + BroadcastGeneration}
}
func (s BroadcastIndex) Write(ctx context.Context, d BroadcastDocument) error {
	return s.WriteBatch(ctx, []BroadcastDocument{d})
}

func (s BroadcastIndex) WriteBatch(ctx context.Context, docs []BroadcastDocument) error {
	values := make([]Mutation, 0, len(docs))
	for _, d := range docs {
		values = append(values, Mutation{d.ItemID, "item", d.Version, d})
	}
	return s.Forward().PutBatch(ctx, values)
}

// Load allocates the write fence before the source snapshot, so concurrent
// periodic/event/read-through loads cannot overwrite a newer materialization.
func (s BroadcastIndex) Load(ctx context.Context, ids []int64) (out map[int64]BroadcastDocument, resultErr error) {

	if len(ids) == 0 {
		return map[int64]BroadcastDocument{}, nil
	}
	if s.DB == nil || len(ids) > 1000 {
		return nil, fmt.Errorf("invalid broadcast source read")
	}
	f := s.Forward()
	fence, err := f.AllocateVersion(ctx)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ItemID, AuthorID, CreatedAt, UpdatedAt, GroupID                           int64
		Active                                                                    bool
		Content, Summary, URL, BroadcastType, SourceType, Lang, ExpireTime, Slots string
		QualityScore                                                              float64
	}
	err = s.DB.WithContext(ctx).Raw(`SELECT r.item_id,r.author_agent_id AS author_id,r.raw_content AS content,r.raw_url AS url,r.created_at,
 p.updated_at,p.status=3 AS active,p.summary,p.broadcast_type,p.source_type,p.lang,p.expire_time,p.group_id,p.quality_score,p.retrieval_slots::text AS slots
 FROM raw_items r JOIN processed_items p USING(item_id) WHERE r.item_id IN ?`, ids).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	values := make(map[int64]BroadcastDocument, len(ids))
	for _, id := range ids {
		values[id] = BroadcastDocument{ItemID: id, Version: fence}
	}
	for _, r := range rows {
		var slots searchindex.Slots
		if err := json.Unmarshal([]byte(r.Slots), &slots); err != nil {
			return nil, err
		}
		if r.Lang != "" {
			slots.Lang = []string{r.Lang}
		}
		values[r.ItemID] = BroadcastDocument{ItemID: r.ItemID, Version: fence, AuthorID: r.AuthorID, Active: r.Active, ContentHash: BroadcastContentHash(r.ItemID, r.AuthorID, r.Content+"\n"+r.Summary, slots), URL: r.URL, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, GroupID: r.GroupID, BroadcastType: r.BroadcastType, SourceType: r.SourceType, Lang: r.Lang, ExpireTime: r.ExpireTime, QualityScore: r.QualityScore, Slots: slots}
	}
	docs := make([]BroadcastDocument, 0, len(values))
	for _, d := range values {
		docs = append(docs, d)
	}
	if err := s.WriteBatch(ctx, docs); err != nil {
		return nil, err
	}
	// Read accepted values: a later source read may already have won the fence.
	return s.read(ctx, ids)
}
func (s BroadcastIndex) read(ctx context.Context, ids []int64) (map[int64]BroadcastDocument, error) {
	views, err := s.Forward().readComponents(ctx, ids, "item")
	if err != nil {
		return nil, err
	}
	out, err := decodeRows[BroadcastDocument](views["item"])
	if err != nil {
		return nil, err
	}
	for id, d := range out {
		if d.ItemID != id || d.Version <= 0 {
			return nil, fmt.Errorf("invalid broadcast feature projection")
		}
	}

	return out, nil
}

// Read batches the forward lookup and bounded source repair. Missing/expired
// features come from DB, never ES; Redis/source failures remain explicit errors.
func (s BroadcastIndex) Read(ctx context.Context, ids []int64) (map[int64]BroadcastDocument, error) {
	rows, err := s.read(ctx, ids)
	if err != nil {
		return nil, err
	}
	missing := []int64{}
	for _, id := range ids {
		if d, ok := rows[id]; !ok || d.Active && d.ContentHash == "" {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		loaded, err := s.Load(ctx, missing)
		if err != nil {
			return nil, err
		}
		for id, d := range loaded {
			rows[id] = d
		}
	}
	return rows, nil
}
func (s BroadcastIndex) LoadPage(ctx context.Context, cursor int64, limit int) (int64, error) {
	if s.DB == nil || cursor < 0 || limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("invalid broadcast source page")
	}
	var page []struct {
		ItemID     int64
		ExpireTime string
	}
	if err := s.DB.WithContext(ctx).Table("processed_items").Select("item_id,expire_time").Where("item_id > ? AND status=3", cursor).Order("item_id").Limit(limit).Scan(&page).Error; err != nil {
		return 0, err
	}
	if len(page) == 0 {
		return 0, nil
	}
	ids := make([]int64, 0, len(page))
	now := time.Now()
	for _, row := range page {
		expiry := ParseExpiry(row.ExpireTime)
		if expiry == nil || expiry.After(now) {
			ids = append(ids, row.ItemID)
		}
	}
	if len(ids) > 0 {
		if _, err := s.Load(ctx, ids); err != nil {
			return 0, err
		}
	}
	return page[len(page)-1].ItemID, nil
}

// BroadcastContentHash binds recall evidence to searchable source content and
// filters. Ranking attributes deliberately do not participate in this digest.
func BroadcastContentHash(id, author int64, text string, slots searchindex.Slots) string {
	b, _ := json.Marshal(struct {
		ID, Author int64
		Text       string
		Slots      searchindex.Slots
	}{id, author, text, slots})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// ParseExpiry shares the source eligibility contract with online hydration.
func ParseExpiry(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t
		}
	}
	expired := time.UnixMilli(1)
	return &expired
}
