package discovery

import (
	"context"
	"gorm.io/gorm"
)

// Store retains cleanup for historical execution rows. New executions record
// their snapshots through the existing asynchronous replay stream.
type Store struct{ DB *gorm.DB }

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
