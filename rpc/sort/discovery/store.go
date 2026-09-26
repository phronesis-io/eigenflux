package discovery

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
)

type Store struct{ DB *gorm.DB }
type row struct {
	ContextID   int64 `gorm:"primaryKey"`
	AgentID     int64
	Persistence string
	InputOrigin string
	State       string
	Priority    float64
	SourceKind  string
	Revision    int64
	Compiled    string `gorm:"type:jsonb"`
	Embedding   []byte
	DeadlineMS  *int64 `gorm:"column:deadline_ms"`
	ExpiresAt   int64
	CreatedAt   int64
	UpdatedAt   int64
}

func (row) TableName() string { return "discovery_contexts" }
func encode(c Context) (row, error) {
	raw, e := json.Marshal(c)
	if e != nil {
		return row{}, e
	}
	v, e := json.Marshal(c.Vector)
	if e != nil {
		return row{}, e
	}
	r := row{ContextID: c.ID, AgentID: c.OwnerID, Persistence: c.Persistence, InputOrigin: c.Origin, State: c.State, Priority: c.Priority, Revision: c.Revision, Compiled: string(raw), Embedding: v, DeadlineMS: c.Filters.DeadlineMS, ExpiresAt: c.ExpiresAt, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	if len(c.Kinds) == 1 {
		r.SourceKind = string(c.Kinds[0])
	}
	return r, nil
}

// Create persists execution snapshots only. Need capture and lifecycle belong to pkg/need.
func (s Store) Create(ctx context.Context, c Context) (Context, error) {
	if c.Persistence != "ephemeral" {
		return Context{}, Invalid("persistence", "execution_snapshot_required")
	}
	r, err := encode(c)
	if err != nil {
		return c, err
	}
	return c, s.DB.WithContext(ctx).Create(&r).Error
}
func (s Store) Prune(ctx context.Context, now int64) error {
	for {
		r := s.DB.WithContext(ctx).Exec("DELETE FROM discovery_contexts WHERE context_id IN (SELECT context_id FROM discovery_contexts WHERE persistence='ephemeral' AND expires_at < ? ORDER BY expires_at LIMIT 1000)", now)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected < 1000 {
			return nil
		}
	}
}
