package consolev2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

type maintenanceEvent struct {
	EventID        string `json:"event_id"`
	EventAt        int64  `json:"event_at"`
	AttemptID      string `json:"attempt_id"`
	Component      string `json:"component"`
	Trigger        string `json:"trigger"`
	Phase          string `json:"phase"`
	Result         string `json:"result"`
	Host           string `json:"host,omitempty"`
	Mode           string `json:"mode,omitempty"`
	FromVersion    string `json:"from_version,omitempty"`
	ToVersion      string `json:"to_version,omitempty"`
	RunningVersion string `json:"running_version,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
}

var maintenanceID = regexp.MustCompile(`^[a-f0-9]{32,64}$`)
var maintenanceValue = regexp.MustCompile(`^[A-Za-z0-9_.+-]{0,128}$`)

func maintenanceOneOf(value string, values ...string) bool {
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}
func validateMaintenanceEvent(e maintenanceEvent, now time.Time) error {
	if !maintenanceID.MatchString(e.EventID) || !maintenanceID.MatchString(e.AttemptID) {
		return fmt.Errorf("invalid maintenance event identity")
	}
	if e.EventAt < now.Add(-7*24*time.Hour).UnixMilli() || e.EventAt > now.Add(5*time.Minute).UnixMilli() {
		return fmt.Errorf("maintenance event outside retention window")
	}
	if !maintenanceOneOf(e.Component, "cli", "skills", "plugin", "scheduler") || !maintenanceOneOf(e.Trigger, "auto", "manual", "adoption") || !maintenanceOneOf(e.Phase, "check", "download", "verify", "probe", "install", "execute", "adoption", "load", "read", "migration") {
		return fmt.Errorf("invalid maintenance category")
	}
	if !maintenanceOneOf(e.Result, "started", "no_update", "installed", "executed", "runtime_ready", "loaded", "rules_read", "verified", "failed", "rolled_back", "restart_required", "blocked", "waiting_online", "not_installed") {
		return fmt.Errorf("invalid maintenance result")
	}
	if (e.Result == "runtime_ready" || e.Result == "executed") && e.Component != "cli" || e.Result == "rules_read" && e.Component != "skills" || e.Result == "loaded" && e.Component != "plugin" || e.Result == "verified" && e.Component != "scheduler" {
		return fmt.Errorf("result does not belong to component")
	}
	if !maintenanceOneOf(e.Mode, "", "skill", "plugin") {
		return fmt.Errorf("invalid maintenance integration mode")
	}
	for _, v := range []string{e.Host, e.FromVersion, e.ToVersion, e.RunningVersion, e.ErrorCode} {
		if !maintenanceValue.MatchString(v) {
			return fmt.Errorf("invalid maintenance metadata")
		}
	}
	if (e.Result == "runtime_ready" || e.Result == "executed" || e.Result == "loaded") && (e.ToVersion == "" || e.RunningVersion != e.ToVersion) {
		return fmt.Errorf("runtime observation requires matching target and running version")
	}
	if e.Result == "rules_read" && e.ToVersion == "" {
		return fmt.Errorf("rules read requires the observed revision")
	}
	if e.DurationMS < 0 || e.DurationMS > int64(24*time.Hour/time.Millisecond) {
		return fmt.Errorf("invalid maintenance duration")
	}
	return nil
}

// recordMaintenanceBatch is intentionally separate from Console telemetry. Agent
// identity is provided by agentAuth(settings:write), never by request properties.
func (s *Service) recordMaintenanceBatch(_ context.Context, c *app.RequestContext) {
	id, ok := agentID(c)
	if !ok || id <= 0 {
		fail(c, http.StatusUnauthorized, "AGENT_REQUIRED", "Agent authentication is required", nil)
		return
	}
	now := time.Now()
	if !s.allowTelemetryRequest("maintenance:"+strconv.FormatInt(id, 10), now) {
		fail(c, http.StatusTooManyRequests, "MAINTENANCE_RATE_LIMITED", "maintenance request rate exceeded", nil)
		return
	}
	if len(c.Request.Body()) > 128<<10 {
		fail(c, http.StatusBadRequest, "INVALID_MAINTENANCE_BATCH", "maintenance batch too large", nil)
		return
	}
	var req struct {
		Events []maintenanceEvent `json:"events"`
	}
	decoder := json.NewDecoder(bytes.NewReader(c.Request.Body()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || len(req.Events) == 0 || len(req.Events) > 50 {
		fail(c, http.StatusBadRequest, "INVALID_MAINTENANCE_BATCH", "expected 1 to 50 maintenance events with known fields", nil)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		fail(c, http.StatusBadRequest, "INVALID_MAINTENANCE_BATCH", "unexpected trailing JSON", nil)
		return
	}
	rows := make([]telemetryEventRequest, 0, len(req.Events))
	for _, event := range req.Events {
		if err := validateMaintenanceEvent(event, now); err != nil {
			fail(c, http.StatusBadRequest, "INVALID_MAINTENANCE_EVENT", err.Error(), nil)
			return
		}
		raw, _ := json.Marshal(event)
		properties := map[string]interface{}{}
		_ = json.Unmarshal(raw, &properties)
		delete(properties, "event_id")
		delete(properties, "event_at")
		// Namespace the global table key by authenticated Agent. One Agent cannot
		// pre-empt another's idempotency key, even when both share an installation.
		key := sha256.Sum256([]byte(strconv.FormatInt(id, 10) + "\x00" + event.EventID))
		rows = append(rows, telemetryEventRequest{EventID: "maintenance-" + hex.EncodeToString(key[:]), EventType: "maintenance_attempt", EventAt: event.EventAt, Properties: properties})
	}
	encoded, _ := json.Marshal(rows)
	result := s.db.Exec(`INSERT INTO telemetry_events_v2
 (event_id,agent_id,install_session_id,console_session_id,event_type,properties,event_at,created_at,expires_at)
 SELECT event_id, ?, NULL, NULL, event_type, properties, event_at, ?, ?
 FROM jsonb_to_recordset(?::jsonb) AS event(event_id text,event_type text,event_at bigint,properties jsonb)
 ON CONFLICT (event_id) DO NOTHING`, id, now.UnixMilli(), now.Add(30*24*time.Hour).UnixMilli(), string(encoded))
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "MAINTENANCE_WRITE_FAILED", "could not persist maintenance events", nil)
		return
	}
	reply(c, http.StatusAccepted, map[string]interface{}{"accepted_events": result.RowsAffected})
}
