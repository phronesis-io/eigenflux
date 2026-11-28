package consolev2

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const installEventRetention = 180 * 24 * time.Hour

var installSessionIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var installProofPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// These are client observations, never grants of business authority. Server
// completion facts have a separate namespace that neither endpoint accepts.
var installClientEvents = map[string]bool{
	"install_started": true, "host_detected": true,
	"component_install": true, "component_verify": true,
	"permission_scheduling": true, "permission_execution": true,
	"host_restart": true, "profile_mode": true, "profile_read_permission": true,
	"profile_draft": true, "identity_local": true, "scheduler_setup": true,
	"credentials_saved": false, "console_link_delivered": false,
	"install_receipt_sent": false, "install_receipt_response": false,
	"baseline_feed": false, "attention_prefill": false, "onboarding_unlocked": false,
	"first_check_requested": false, "first_check": false, "first_check_result": false,
}

type installLifecycleEvent struct {
	EventID     string `json:"event_id"`
	EventAt     int64  `json:"event_at"`
	EventType   string `json:"event_type"`
	Result      string `json:"result"`
	AttemptID   string `json:"attempt_id,omitempty"`
	Component   string `json:"component,omitempty"`
	RuntimeName string `json:"runtime_name,omitempty"`
	RuntimeMode string `json:"runtime_mode,omitempty"`
	Choice      string `json:"choice,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Count       *int   `json:"count,omitempty"`
}

type installLifecycleBatch struct {
	InstallSessionID string                  `json:"install_session_id"`
	Events           []installLifecycleEvent `json:"events"`
}

func validateInstallLifecycleEvent(e installLifecycleEvent, authenticated bool, now time.Time) error {
	preAuth, known := installClientEvents[e.EventType]
	if !known || (!authenticated && !preAuth) || !maintenanceID.MatchString(e.EventID) {
		return errors.New("invalid event identity or type")
	}
	if e.EventAt < now.Add(-7*24*time.Hour).UnixMilli() || e.EventAt > now.Add(time.Minute).UnixMilli() {
		return errors.New("event time outside accepted window")
	}
	if !maintenanceOneOf(e.Result, "started", "succeeded", "failed", "declined", "skipped", "pending") ||
		(e.AttemptID != "" && !maintenanceID.MatchString(e.AttemptID)) ||
		!maintenanceOneOf(e.Component, "", "cli", "skills", "plugin", "scheduler", "identity", "console") ||
		!maintenanceOneOf(e.RuntimeMode, "", "skill", "plugin", "cli", "unknown") ||
		!maintenanceOneOf(e.RuntimeName, "", "unknown", "codex", "openclaw", "workbuddy", "hermes", "deepseek", "deepseek-harness", "dsh", "claude-code", "claude", "cursor", "opencode", "gemini-cli", "gemini", "qoder", "trae", "kilo", "roo", "cline", "windsurf", "antigravity", "kimi", "kiro", "jarvis") ||
		!maintenanceOneOf(e.Reason, "", "network_error", "timeout", "permission_denied", "user_declined", "user_cancelled", "not_applicable", "no_candidates", "no_content", "validation_failed", "host_unavailable", "authentication_failed", "server_error", "unknown_failure", "restart_required", "already_configured", "manual_selected") {
		return errors.New("invalid structured observation")
	}
	if e.Choice != "" && (e.EventType != "profile_mode" || !maintenanceOneOf(e.Choice, "prefill", "manual")) {
		return errors.New("invalid profile choice")
	}
	if e.Count != nil && (*e.Count < 0 || *e.Count > 10000 || !maintenanceOneOf(e.EventType, "baseline_feed", "attention_prefill", "first_check")) {
		return errors.New("invalid observation count")
	}
	return nil
}

func (e installLifecycleEvent) properties() map[string]interface{} {
	p := map[string]interface{}{}
	for key, value := range map[string]string{"attempt_id": e.AttemptID, "component": e.Component,
		"runtime_name": e.RuntimeName, "runtime_mode": e.RuntimeMode, "choice": e.Choice, "reason": e.Reason} {
		if value != "" {
			p[key] = value
		}
	}
	if e.Count != nil {
		p["count"] = *e.Count
	}
	return p
}

var (
	errInstallProof    = errors.New("invalid install session proof")
	errInstallBinding  = errors.New("install session belongs to another Agent")
	errInstallConflict = errors.New("event identity already used with different data")
	errInstallQuota    = errors.New("install telemetry quota exceeded")
	errInstallExpired  = errors.New("install session expired")
)

type installLifecycleSession struct {
	ProofHash  string
	AgentID    *int64
	ExpiresAt  int64
	EventCount int
}

func consumeInstallQuota(tx *gorm.DB, key string, day int64, maximum int) error {
	result := tx.Exec(`INSERT INTO install_lifecycle_limits(rate_key,window_at,used) VALUES (?,?,1)
		ON CONFLICT (rate_key,window_at) DO UPDATE SET used=install_lifecycle_limits.used+1
		WHERE install_lifecycle_limits.used < ?`, key, day, maximum)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errInstallQuota
	}
	return nil
}

// The row lock makes proof checks, one-time identity binding, event insertion and
// the per-session quota one atomic operation, including concurrent first writes.
func lockInstallLifecycleSession(tx *gorm.DB, sessionID, proof, ipHash string, actor *int64, now int64, create bool) (installLifecycleSession, error) {
	var row installLifecycleSession
	if create {
		result := tx.Exec(`INSERT INTO install_lifecycle_sessions
			(install_session_id,proof_hash,created_at,expires_at) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`,
			sessionID, hashString(proof), now, now+int64(installEventRetention/time.Millisecond))
		if result.Error != nil {
			return row, result.Error
		}
		if result.RowsAffected == 1 {
			day := now / int64(24*time.Hour/time.Millisecond)
			if err := consumeInstallQuota(tx, "global", day, 10000); err != nil {
				return row, err
			}
			if err := consumeInstallQuota(tx, "ip:"+ipHash, day, 256); err != nil {
				return row, err
			}
		}
	}
	result := tx.Raw(`SELECT proof_hash,agent_id,expires_at,event_count FROM install_lifecycle_sessions
		WHERE install_session_id=? FOR UPDATE`, sessionID).Scan(&row)
	if result.Error != nil {
		return row, result.Error
	}
	if row.ProofHash == "" || subtle.ConstantTimeCompare([]byte(row.ProofHash), []byte(hashString(proof))) != 1 {
		return row, errInstallProof
	}
	if row.ExpiresAt <= now {
		return row, errInstallExpired
	}
	if actor != nil {
		if row.AgentID != nil && *row.AgentID != *actor {
			return row, errInstallBinding
		}
		if row.AgentID == nil {
			if err := tx.Exec(`UPDATE install_lifecycle_sessions SET agent_id=?,bound_at=? WHERE install_session_id=? AND agent_id IS NULL`, *actor, now, sessionID).Error; err != nil {
				return row, err
			}
			row.AgentID = actor
		}
	}
	return row, nil
}

func (s *Service) recordAnonymousInstallLifecycle(ctx context.Context, c *app.RequestContext) {
	s.recordInstallLifecycle(ctx, c, nil)
}

func (s *Service) recordAuthenticatedInstallLifecycle(ctx context.Context, c *app.RequestContext) {
	id, ok := agentID(c)
	if !ok || id <= 0 {
		fail(c, 401, "AGENT_AUTH_REQUIRED", "Agent authentication required", nil)
		return
	}
	s.recordInstallLifecycle(ctx, c, &id)
}

func (s *Service) recordInstallLifecycle(ctx context.Context, c *app.RequestContext, actor *int64) {
	now := time.Now()
	ipHash := s.clientIPHash(c)
	if !s.allowTelemetryRequest("install:"+ipHash, now) {
		fail(c, 429, "INSTALL_TELEMETRY_RATE_LIMITED", "request rate exceeded", nil)
		return
	}
	proof := string(c.GetHeader("X-Install-Session-Proof"))
	var req installLifecycleBatch
	decoder := json.NewDecoder(bytes.NewReader(c.Request.Body()))
	decoder.DisallowUnknownFields()
	if len(c.Request.Body()) > 32<<10 || !installProofPattern.MatchString(proof) || decoder.Decode(&req) != nil ||
		!installSessionIDPattern.MatchString(req.InstallSessionID) || len(req.Events) < 1 || len(req.Events) > 20 || decoder.Decode(&struct{}{}) != io.EOF {
		fail(c, 400, "INVALID_INSTALL_TELEMETRY", "expected a bounded batch with known fields and a session proof", nil)
		return
	}
	for _, event := range req.Events {
		if err := validateInstallLifecycleEvent(event, actor != nil, now); err != nil {
			fail(c, 400, "INVALID_INSTALL_TELEMETRY", err.Error(), nil)
			return
		}
	}
	accepted := int64(0)
	bound := false
	// Silent SQL logging is deliberate: proof hashes and optional request metadata
	// must not leak through GORM diagnostics. Errors returned to clients are fixed.
	err := s.db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		row, err := lockInstallLifecycleSession(tx, req.InstallSessionID, proof, ipHash, actor, now.UnixMilli(), true)
		if err != nil {
			return err
		}
		bound = row.AgentID != nil
		for _, event := range req.Events {
			payload, _ := json.Marshal(event)
			properties, _ := json.Marshal(event.properties())
			key, fingerprint := hashString(req.InstallSessionID+":"+event.EventID), hashString(string(payload))
			result := tx.Exec(`INSERT INTO install_lifecycle_events
				(event_key,event_id,install_session_id,agent_id,event_type,result,evidence,properties,payload_hash,event_at,received_at,expires_at)
				VALUES (?,?,?,?,?,?,'client_reported',?::jsonb,?,?,?,?) ON CONFLICT(event_key) DO NOTHING`,
				key, event.EventID, req.InstallSessionID, actor, event.EventType, event.Result, string(properties), fingerprint,
				event.EventAt, now.UnixMilli(), now.Add(installEventRetention).UnixMilli())
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				var prior string
				if err := tx.Raw(`SELECT payload_hash FROM install_lifecycle_events WHERE event_key=?`, key).Scan(&prior).Error; err != nil {
					return err
				}
				if prior != fingerprint {
					return errInstallConflict
				}
			}
			accepted += result.RowsAffected
		}
		if row.EventCount+int(accepted) > 512 {
			return errInstallQuota
		}
		return tx.Exec(`UPDATE install_lifecycle_sessions SET event_count=event_count+? WHERE install_session_id=?`, accepted, req.InstallSessionID).Error
	})
	if err != nil {
		status, code := http.StatusServiceUnavailable, "INSTALL_TELEMETRY_UNAVAILABLE"
		switch {
		case errors.Is(err, errInstallProof):
			status, code = 403, "INSTALL_SESSION_PROOF_INVALID"
		case errors.Is(err, errInstallBinding), errors.Is(err, errInstallConflict):
			status, code = 409, "INSTALL_TELEMETRY_CONFLICT"
		case errors.Is(err, errInstallQuota):
			status, code = 429, "INSTALL_TELEMETRY_QUOTA_EXCEEDED"
		case errors.Is(err, errInstallExpired):
			status, code = 410, "INSTALL_SESSION_EXPIRED"
		}
		fail(c, status, code, "installation observation was not accepted", nil)
		return
	}
	reply(c, http.StatusAccepted, map[string]interface{}{"accepted_events": accepted, "install_session_id": req.InstallSessionID, "bound": bound})
}
