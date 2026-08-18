package consolev2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"
)

const telemetryBucketMS = int64(2 * time.Minute / time.Millisecond)

var telemetryIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

var telemetryEventTypes = map[string]bool{
	"install_started": true, "agent_provisioned": true, "onboarding_draft_saved": true,
	"handoff_created": true, "handoff_opened": true, "console_session_created": true,
	"dashboard_first_render": true, "email_binding_started": true, "email_binding_completed": true,
	"goal_edited": true, "intent_action_edited": true, "attention_item_opened": true,
	"agent_command_created": true, "agent_command_completed": true,
	"feed_item_helpful": true, "feed_item_dismissed": true,
}

var telemetryPropertyKeys = map[string]bool{
	"route": true, "step": true, "source": true, "entity_type": true,
	"entity_id": true, "result": true, "action": true,
}

type telemetryRateState struct {
	WindowStart time.Time
	Requests    int
}

type telemetryEventRequest struct {
	EventID    string                 `json:"event_id"`
	EventType  string                 `json:"event_type"`
	EventAt    int64                  `json:"event_at"`
	Properties map[string]interface{} `json:"properties"`
}

type telemetryUsageRequest struct {
	SessionID         string `json:"session_id"`
	TimeBucket        int64  `json:"time_bucket"`
	VisibleDurationMS int64  `json:"visible_duration_ms"`
	FirstEventAt      int64  `json:"first_event_at"`
	LastEventAt       int64  `json:"last_event_at"`
}

type telemetryBatchRequest struct {
	Events []telemetryEventRequest `json:"events"`
	Usage  *telemetryUsageRequest  `json:"usage,omitempty"`
}

func (s *Service) allowTelemetryRequest(sessionID string, now time.Time) bool {
	s.telemetryMu.Lock()
	defer s.telemetryMu.Unlock()
	if s.telemetryNextSweep.IsZero() || !now.Before(s.telemetryNextSweep) {
		cutoff := now.Add(-10 * time.Minute)
		for key, candidate := range s.telemetryRates {
			if candidate.WindowStart.Before(cutoff) {
				delete(s.telemetryRates, key)
			}
		}
		s.telemetryNextSweep = now.Add(time.Minute)
	}
	// Bound process memory during a bot/session-cardinality spike. Existing
	// sessions keep working; new telemetry is expendable and may be dropped.
	state, exists := s.telemetryRates[sessionID]
	if !exists && len(s.telemetryRates) >= 200000 {
		return false
	}
	if state.WindowStart.IsZero() || now.Sub(state.WindowStart) >= time.Minute {
		state = telemetryRateState{WindowStart: now}
	}
	if state.Requests >= 15 {
		return false
	}
	state.Requests++
	s.telemetryRates[sessionID] = state
	return true
}

func validateTelemetryEvent(event telemetryEventRequest, now int64) error {
	if !telemetryIDPattern.MatchString(event.EventID) || !telemetryEventTypes[event.EventType] {
		return errors.New("invalid telemetry event identity or type")
	}
	if event.EventAt < now-int64(24*time.Hour/time.Millisecond) || event.EventAt > now+int64(5*time.Minute/time.Millisecond) {
		return errors.New("telemetry event time is outside the accepted window")
	}
	for key := range event.Properties {
		if !telemetryPropertyKeys[key] {
			return errors.New("telemetry property is not allowed")
		}
	}
	encoded, _ := json.Marshal(event.Properties)
	if len(encoded) > 4096 {
		return errors.New("telemetry properties are too large")
	}
	return nil
}

func validateTelemetryUsage(usage *telemetryUsageRequest, now int64) error {
	if usage == nil {
		return nil
	}
	if !telemetryIDPattern.MatchString(usage.SessionID) || usage.TimeBucket <= 0 || usage.TimeBucket%telemetryBucketMS != 0 {
		return errors.New("invalid usage session or time bucket")
	}
	if usage.VisibleDurationMS < 0 || usage.VisibleDurationMS > telemetryBucketMS || usage.FirstEventAt > usage.LastEventAt {
		return errors.New("invalid visible duration")
	}
	if usage.FirstEventAt < now-int64(24*time.Hour/time.Millisecond) || usage.LastEventAt > now+int64(5*time.Minute/time.Millisecond) {
		return errors.New("usage time is outside the accepted window")
	}
	if usage.FirstEventAt < usage.TimeBucket || usage.FirstEventAt >= usage.TimeBucket+telemetryBucketMS || usage.LastEventAt > usage.TimeBucket+telemetryBucketMS+5000 {
		return errors.New("usage timestamps do not match the time bucket")
	}
	return nil
}

func (s *Service) recordTelemetryBatch(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	sessionValue, ok := c.Get("console_session_id")
	consoleSessionID, sessionOK := sessionValue.(string)
	if !ok || !sessionOK || consoleSessionID == "" {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		return
	}
	if !s.allowTelemetryRequest(consoleSessionID, time.Now()) {
		fail(c, http.StatusTooManyRequests, "TELEMETRY_RATE_LIMITED", "telemetry request rate exceeded", nil)
		return
	}
	var req telemetryBatchRequest
	if err := decodeBody(c, &req); err != nil || len(req.Events) > 50 || (len(req.Events) == 0 && req.Usage == nil) {
		fail(c, http.StatusBadRequest, "INVALID_TELEMETRY_BATCH", "batch must contain up to 50 events or one usage bucket", nil)
		return
	}
	now := time.Now().UnixMilli()
	for index := range req.Events {
		if req.Events[index].Properties == nil {
			req.Events[index].Properties = map[string]interface{}{}
		}
		if err := validateTelemetryEvent(req.Events[index], now); err != nil {
			fail(c, http.StatusBadRequest, "INVALID_TELEMETRY_EVENT", err.Error(), map[string]interface{}{"index": index})
			return
		}
	}
	if err := validateTelemetryUsage(req.Usage, now); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_USAGE_BUCKET", err.Error(), nil)
		return
	}
	eventsJSON, _ := json.Marshal(req.Events)
	expiresAt := now + int64(30*24*time.Hour/time.Millisecond)
	accepted := int64(0)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if len(req.Events) > 0 {
			result := tx.Exec(`INSERT INTO telemetry_events_v2
				(event_id, agent_id, install_session_id, console_session_id, event_type, properties, event_at, created_at, expires_at)
				SELECT event_id, ?, NULL, ?, event_type, properties, event_at, ?, ?
				FROM jsonb_to_recordset(?::jsonb) AS event(event_id text, event_type text, event_at bigint, properties jsonb)
				ON CONFLICT (event_id) DO NOTHING`, agentIDValue, consoleSessionID, now, expiresAt, string(eventsJSON))
			if result.Error != nil {
				return result.Error
			}
			accepted = result.RowsAffected
		}
		if req.Usage != nil {
			usage := req.Usage
			return tx.Exec(`INSERT INTO console_usage_sessions
				(session_id, time_bucket, agent_id, visible_duration_ms, first_event_at, last_event_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (session_id, time_bucket) DO UPDATE SET
					agent_id = EXCLUDED.agent_id,
					visible_duration_ms = GREATEST(console_usage_sessions.visible_duration_ms, EXCLUDED.visible_duration_ms),
					first_event_at = LEAST(console_usage_sessions.first_event_at, EXCLUDED.first_event_at),
					last_event_at = GREATEST(console_usage_sessions.last_event_at, EXCLUDED.last_event_at),
					updated_at = EXCLUDED.updated_at`, usage.SessionID, usage.TimeBucket, agentIDValue,
				usage.VisibleDurationMS, usage.FirstEventAt, usage.LastEventAt, now).Error
		}
		return nil
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "TELEMETRY_WRITE_FAILED", "could not persist telemetry batch", nil)
		return
	}
	reply(c, http.StatusAccepted, map[string]interface{}{"accepted_events": accepted, "usage_recorded": req.Usage != nil})
}
