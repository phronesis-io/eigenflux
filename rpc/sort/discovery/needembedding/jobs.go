package needembedding

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"time"

	"eigenflux_server/pkg/need"
)

type Job struct {
	InputID       int64
	IntentID      int64
	IntentVersion int64
	Input         json.RawMessage
	LeaseToken    string
	Attempts      int
}

// Claim derives work from current inputs, including rows saved before this
// worker was deployed. Generations coexist safely during a rolling upgrade.
func (c *Cache) Claim(ctx context.Context, now int64) (Job, error) {
	var job Job
	token := rand.Text()
	err := c.DB.WithContext(ctx).Raw(`WITH candidate AS (
 SELECT n.need_input_id FROM current_need_inputs n
 LEFT JOIN need_embedding_jobs j ON j.need_input_id=n.need_input_id AND j.generation=?
 WHERE (n.input->'constraints'->>'deadline_ms' IS NULL OR (n.input->'constraints'->>'deadline_ms')::bigint>?)
 AND (j.need_input_id IS NULL OR (j.lease_until<=? AND j.next_attempt_at<=?))
 ORDER BY COALESCE(j.next_attempt_at,0),n.created_at,n.need_input_id LIMIT 1
 ), claimed AS (
 INSERT INTO need_embedding_jobs(need_input_id,generation,next_attempt_at,lease_until,lease_token,attempts)
 SELECT need_input_id,?,0,?,?,1 FROM candidate
 ON CONFLICT(need_input_id,generation) DO UPDATE SET lease_until=EXCLUDED.lease_until,
 lease_token=EXCLUDED.lease_token,attempts=need_embedding_jobs.attempts+1,ready=false
 WHERE need_embedding_jobs.lease_until<=? AND need_embedding_jobs.next_attempt_at<=?
 RETURNING need_input_id,lease_token,attempts)
 SELECT n.need_input_id AS input_id,n.intent_id,n.intent_version,n.input,c.lease_token,c.attempts
 FROM claimed c JOIN current_need_inputs n USING(need_input_id)`, c.Generation(), now, now, now, c.Generation(), now+LeaseDuration.Milliseconds(), token, now, now).Scan(&job).Error
	return job, err
}
func (j Job) Snapshot() need.Snapshot {
	return need.Snapshot{InputID: j.InputID, IntentID: j.IntentID, IntentVersion: j.IntentVersion, Input: j.Input}
}

func (c *Cache) Finish(ctx context.Context, j Job, ready bool, now int64) error {
	delay := RefreshInterval
	if !ready {
		delay = 5 * time.Second
		for i := 1; i < j.Attempts && delay < 5*time.Minute; i++ {
			delay *= 2
		}
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
	}
	attempts := j.Attempts
	if ready {
		attempts = 0
	}
	return c.DB.WithContext(ctx).Exec(`UPDATE need_embedding_jobs SET ready=?,next_attempt_at=?,lease_until=0,lease_token='',attempts=?
 WHERE need_input_id=? AND generation=? AND lease_token=?`, ready, now+delay.Milliseconds(), attempts, j.InputID, c.Generation(), j.LeaseToken).Error
}
