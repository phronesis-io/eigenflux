package agentidentity

import (
	"eigenflux_server/pkg/metrics"
	"errors"
	"gorm.io/gorm"
)

// InsertAnonymous creates identity facts only. The caller owns the transaction,
// profile initialization and its own authorization; no personal scopes are granted.
func InsertAnonymous(tx *gorm.DB, id int64, alias, name string, now int64) error {
	for attempt := 0; attempt < 100; attempt++ {
		shortID, err := GenerateShortID()
		if err != nil {
			return err
		}
		result := tx.Exec(`INSERT INTO agents
			(agent_id, short_id, email, email_kind, agent_name, bio, created_at, updated_at)
			VALUES (?, ?, ?, 'internal_alias', ?, '', ?, ?)
			ON CONFLICT (short_id) WHERE short_id IS NOT NULL DO NOTHING`, id, shortID, alias, name, now, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		metrics.AgentShortIDGenerationCollisionTotal.Inc()
	}
	metrics.AgentShortIDGenerationFailureTotal.Inc()
	return errors.New("short-id collision retry budget exhausted")
}
