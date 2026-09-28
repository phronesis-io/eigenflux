package featureindex

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// AgentIndex owns public Card forward features and DB materialization.
type AgentIndex struct {
	Redis     *redis.Client
	DB        *gorm.DB
	IndexName string
}

func (i AgentIndex) View() string       { return Agent }
func (i AgentIndex) Generation() string { return i.IndexName }
func (i AgentIndex) Forward() Forward {
	return Forward{Redis: i.Redis, Namespace: "agent:" + i.IndexName}
}

func (i AgentIndex) Write(ctx context.Context, d AgentDocument) error {
	return i.WriteBatch(ctx, []AgentDocument{d})
}

func (i AgentIndex) WriteBatch(ctx context.Context, docs []AgentDocument) error {
	values := make([]Mutation, 0, len(docs))
	for _, d := range docs {
		d.Embedding = nil
		values = append(values, Mutation{d.AgentID, "card", d.ProjectionVersion, d})
	}
	return i.Forward().PutBatch(ctx, values)
}

func (i AgentIndex) Read(ctx context.Context, ids []int64) (map[int64]AgentDocument, error) {
	views, err := i.Forward().readComponents(ctx, ids, "card")
	if err != nil {
		return nil, err
	}
	out, err := decodeRows[AgentDocument](views["card"])
	if err != nil {
		return nil, err
	}
	for id, d := range out {
		if d.AgentID != id || d.Version <= 0 || d.ProjectionVersion <= 0 {
			return nil, fmt.Errorf("invalid Agent forward projection")
		}
	}

	return out, nil
}

// LoadPage repairs scalar features from committed Cards without a model
// call or ES read/write. Card projection fences remain the write authority.
func (p AgentIndex) LoadPage(ctx context.Context, cursor int64, limit int) (nextCursor int64, resultErr error) {

	if p.DB == nil || cursor < 0 || limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("invalid Agent source page")
	}
	var ids []int64
	if err := p.DB.WithContext(ctx).Table("agent_cards c").Joins("JOIN agents a ON a.agent_id=c.agent_id").Where("c.agent_id > ? AND c.public_card_version > 0 AND a.identity_state='active' AND a.profile_completed_at > 0", cursor).Order("c.agent_id").Limit(limit).Pluck("c.agent_id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	rows, err := loadAgents(ctx, p.DB, ids, true)
	if err != nil {
		return 0, err
	}
	if err := p.WriteBatch(ctx, rows); err != nil {
		return 0, err
	}
	return ids[len(ids)-1], nil
}
