package consolev2

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

type todayStatusFacts struct {
	LastHeartbeatAt      int64 `gorm:"column:last_heartbeat_at"`
	FirstScanCompletedAt int64 `gorm:"column:first_scan_completed_at"`
	LastScanAt           int64 `gorm:"column:last_scan_at"`
}

func (s *Service) getTodayStatus(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	now := time.Now().UTC()
	var privateCard string
	if err := s.db.Raw(`SELECT COALESCE(
		(SELECT private_card FROM agent_cards WHERE agent_id = ?),
		'{}'::jsonb
	)::text AS private_card`, agentIDValue).Scan(&privateCard).Error; err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_STATUS_READ_FAILED", "could not load Agent timezone", nil)
		return
	}
	todayStart := todayStartFromPrivateCard(privateCard, now)
	var facts todayStatusFacts
	if err := s.db.Raw(`SELECT
		COALESCE((SELECT MAX(last_heartbeat_at) FROM agent_runtime_leases WHERE agent_id = ?), 0)::bigint AS last_heartbeat_at,
		COALESCE((SELECT MIN(created_at) FROM agent_activity_log WHERE agent_id = ? AND event_type = 'feed_pull' AND created_at >= ?), 0)::bigint AS first_scan_completed_at,
		COALESCE((SELECT MAX(created_at) FROM agent_activity_log WHERE agent_id = ? AND event_type = 'feed_pull' AND created_at >= ?), 0)::bigint AS last_scan_at`,
		agentIDValue, agentIDValue, todayStart, agentIDValue, todayStart).Scan(&facts).Error; err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_STATUS_READ_FAILED", "could not load Today status", nil)
		return
	}
	nowMillis := now.UnixMilli()
	runtimeState := consoleRuntimeState(facts.LastHeartbeatAt, nowMillis)
	firstScanCompleted := facts.FirstScanCompletedAt > 0
	observationState := todayObservationState(false, firstScanCompleted, runtimeState == "active", runtimeState != "not_started")
	firstScanState := "not_started"
	if firstScanCompleted {
		firstScanState = "completed"
	} else if runtimeState == "active" {
		firstScanState = "running"
	}
	freshUntil := int64(0)
	if facts.LastHeartbeatAt > 0 {
		freshUntil = facts.LastHeartbeatAt + consoleRuntimeFreshness.Milliseconds()
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"connection":        map[string]interface{}{"state": "connected"},
		"runtime_state":     runtimeState,
		"last_heartbeat_at": facts.LastHeartbeatAt,
		"fresh_until":       freshUntil,
		"observation": map[string]interface{}{
			"state":                   observationState,
			"first_scan_state":        firstScanState,
			"first_scan_completed_at": facts.FirstScanCompletedAt,
			"last_scan_at":            facts.LastScanAt,
		},
	})
}
