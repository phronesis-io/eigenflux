package discoverydb

import (
	"context"
	"encoding/json"
	"errors"

	"eigenflux_server/pkg/discovery"
	"gorm.io/gorm"
)

type Store struct{ DB *gorm.DB }
type row struct {
	ContextID       int64 `gorm:"primaryKey"`
	AgentID         int64
	Persistence     string
	InputOrigin     string
	State           string
	Priority        float64
	SourceKind      string
	Revision        int64
	Compiled        string `gorm:"type:jsonb"`
	Embedding       []byte
	DeadlineMS      *int64 `gorm:"column:deadline_ms"`
	ExpiresAt       int64
	CreatedAt       int64
	UpdatedAt       int64
	IdempotencyKey  string
	IdempotencyHash string
}

func (row) TableName() string { return "discovery_contexts" }
func encode(c discovery.Context) (row, error) {
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
func decode(r row) (discovery.Context, error) {
	var c discovery.Context
	if e := json.Unmarshal([]byte(r.Compiled), &c); e != nil {
		return c, e
	}
	if len(r.Embedding) > 0 {
		if e := json.Unmarshal(r.Embedding, &c.Vector); e != nil {
			return c, e
		}
	}
	c.State = r.State
	c.Revision = r.Revision
	c.UpdatedAt = r.UpdatedAt
	return c, nil
}
func (s Store) Get(ctx context.Context, owner, id int64, saved bool) (discovery.Context, error) {
	q := s.DB.WithContext(ctx).Where("agent_id = ? AND context_id = ?", owner, id)
	if saved {
		q = q.Where("persistence = 'saved'")
	}
	var r row
	e := q.First(&r).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return discovery.Context{}, discovery.Failure(404, "need_not_found")
	}
	if e != nil {
		return discovery.Context{}, e
	}
	return decode(r)
}
func ownerLock(tx *gorm.DB, owner int64) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", owner).Error
}
func activeCount(tx *gorm.DB, owner, now int64) (int64, error) {
	var n int64
	e := tx.Model(&row{}).Where("agent_id = ? AND persistence = 'saved' AND state = 'active' AND (deadline_ms IS NULL OR deadline_ms > ?)", owner, now).Count(&n).Error
	return n, e
}
func (s Store) Create(ctx context.Context, c discovery.Context, key string) (discovery.Context, error) {
	return s.CreateWithHash(ctx, c, key, c.SpecHash)
}
func (s Store) Idempotent(ctx context.Context, owner int64, key, hash string) (discovery.Context, bool, error) {
	if key == "" {
		return discovery.Context{}, false, nil
	}
	if len(key) > 128 {
		return discovery.Context{}, false, discovery.Invalid("idempotency_key", "too_long")
	}
	var r row
	err := s.DB.WithContext(ctx).Where("agent_id=? AND idempotency_key=?", owner, key).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return discovery.Context{}, false, nil
	}
	if err != nil {
		return discovery.Context{}, false, err
	}
	if r.IdempotencyHash != hash {
		return discovery.Context{}, true, discovery.Failure(409, "idempotency_conflict")
	}
	c, err := decode(r)
	return c, true, err
}
func (s Store) CreateWithHash(ctx context.Context, c discovery.Context, key, hash string) (discovery.Context, error) {
	r, e := encode(c)
	if e != nil {
		return c, e
	}
	if len(key) > 128 {
		return c, discovery.Invalid("idempotency_key", "too_long")
	}
	r.IdempotencyKey = key
	r.IdempotencyHash = hash
	e = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ownerLock(tx, c.OwnerID); err != nil {
			return err
		}
		if key != "" {
			var prior row
			err := tx.Where("agent_id = ? AND idempotency_key = ?", c.OwnerID, key).First(&prior).Error
			if err == nil {
				if prior.IdempotencyHash != hash {
					return discovery.Failure(409, "idempotency_conflict")
				}
				var e error
				c, e = decode(prior)
				return e
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if c.Persistence == "saved" {
			n, err := activeCount(tx, c.OwnerID, c.CreatedAt)
			if err != nil {
				return err
			}
			if n >= 10 {
				return discovery.Failure(409, "active_need_limit")
			}
		}
		return tx.Create(&r).Error
	})
	return c, e
}
func (s Store) Update(ctx context.Context, c discovery.Context, expected int64) (discovery.Context, error) {
	if expected <= 0 {
		return c, discovery.Invalid("expected_revision", "required")
	}
	e := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := ownerLock(tx, c.OwnerID); e != nil {
			return e
		}
		old, e := (Store{tx}).Get(ctx, c.OwnerID, c.ID, true)
		if e != nil {
			return e
		}
		if old.Revision != expected {
			return discovery.Failure(409, "revision_conflict")
		}
		if old.State == "completed" || old.State == "expired" || old.Filters.DeadlineMS != nil && *old.Filters.DeadlineMS <= c.UpdatedAt {
			return discovery.Failure(409, "terminal_need")
		}
		c.CreatedAt = old.CreatedAt
		c.State = old.State
		c.Revision = expected + 1
		r, e := encode(c)
		if e != nil {
			return e
		}
		return tx.Model(&row{}).Where("context_id=? AND agent_id=? AND revision=?", c.ID, c.OwnerID, expected).Updates(map[string]any{"compiled": r.Compiled, "embedding": r.Embedding, "revision": r.Revision, "priority": r.Priority, "source_kind": r.SourceKind, "deadline_ms": r.DeadlineMS, "updated_at": r.UpdatedAt}).Error
	})
	return c, e
}
func (s Store) SetState(ctx context.Context, owner, id, expected int64, state string, now int64) (discovery.Context, error) {
	var c discovery.Context
	if state != "active" && state != "paused" && state != "completed" {
		return c, discovery.Invalid("state", "invalid")
	}
	e := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := ownerLock(tx, owner); e != nil {
			return e
		}
		var e error
		c, e = (Store{tx}).Get(ctx, owner, id, true)
		if e != nil {
			return e
		}
		if expected <= 0 || c.Revision != expected {
			return discovery.Failure(409, "revision_conflict")
		}
		if c.State == "completed" || c.State == "expired" || c.Filters.DeadlineMS != nil && *c.Filters.DeadlineMS <= now {
			return discovery.Failure(409, "terminal_need")
		}
		if c.State == state {
			return nil
		}
		if state == "active" {
			n, e := activeCount(tx, owner, now)
			if e != nil {
				return e
			}
			if n >= 10 {
				return discovery.Failure(409, "active_need_limit")
			}
		}
		c.State = state
		c.Revision++
		c.UpdatedAt = now
		r, e := encode(c)
		if e != nil {
			return e
		}
		return tx.Model(&row{}).Where("agent_id=? AND context_id=?", owner, id).Updates(map[string]any{"state": state, "revision": c.Revision, "compiled": r.Compiled, "updated_at": now}).Error
	})
	return c, e
}
func (s Store) List(ctx context.Context, owner int64, state string, cursor int64, limit int, now int64) ([]discovery.Context, error) {
	if limit < 1 || limit > 101 {
		return nil, discovery.Invalid("limit", "out_of_range")
	}
	q := s.DB.WithContext(ctx).Where("agent_id=? AND persistence='saved'", owner)
	if state != "" {
		switch state {
		case "active":
			q = q.Where("state='active' AND (deadline_ms IS NULL OR deadline_ms>?)", now)
		case "expired":
			q = q.Where("(state='expired' OR (state IN ('active','paused') AND deadline_ms<=?))", now)
		case "paused", "completed":
			q = q.Where("state=?", state)
		default:
			return nil, discovery.Invalid("state", "invalid")
		}
	}
	if cursor > 0 {
		q = q.Where("context_id < ?", cursor)
	}
	var rows []row
	if e := q.Order("context_id DESC").Limit(limit).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := make([]discovery.Context, 0, len(rows))
	for _, r := range rows {
		c, e := decode(r)
		if e != nil {
			return nil, e
		}
		if (c.State == "active" || c.State == "paused") && c.Filters.DeadlineMS != nil && *c.Filters.DeadlineMS <= now {
			c.State = "expired"
		}
		out = append(out, c)
	}
	return out, nil
}
func (s Store) Active(ctx context.Context, owner int64, kinds []discovery.Kind, now int64) ([]discovery.Context, error) {
	var rows []row
	e := s.DB.WithContext(ctx).Where("agent_id=? AND persistence='saved' AND state='active' AND (deadline_ms IS NULL OR deadline_ms>?) AND source_kind IN ?", owner, now, kinds).Order("priority DESC, updated_at DESC, context_id ASC").Limit(5).Find(&rows).Error
	if e != nil {
		return nil, e
	}
	out := []discovery.Context{}
	for _, r := range rows {
		c, e := decode(r)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, nil
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
