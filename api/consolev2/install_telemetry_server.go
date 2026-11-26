package consolev2

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"eigenflux_server/pkg/logger"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/google/uuid"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// observeInstallOperation records the outcome returned by the real handler. It
// never accepts a client assertion of server completion and never changes the
// business response. Existing business rows remain the authoritative state;
// these observations add failed-request and coverage evidence.
func (s *Service) observeInstallOperation(eventType string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Next(ctx)
		status := c.Response.StatusCode()
		id, _ := agentID(c)
		properties := map[string]interface{}{"http_status": status}
		result := "failed"
		if status >= 200 && status < 300 {
			result = "succeeded"
			// Only typed, non-content fields are read from the response. Credentials,
			// draft text, URLs and response error messages are never retained.
			var response struct {
				Data struct {
					AgentID  string `json:"agent_id"`
					Created  *bool  `json:"created"`
					Recorded *bool  `json:"recorded"`
					State    string `json:"state"`
					Revision int64  `json:"revision"`
				} `json:"data"`
			}
			if json.Unmarshal(c.Response.Body(), &response) == nil {
				if eventType == "server_registration" {
					parsed, err := strconv.ParseInt(response.Data.AgentID, 10, 64)
					if err == nil && parsed > 0 {
						id = parsed
					}
					if response.Data.Created != nil {
						properties["created"] = *response.Data.Created
					}
				}
				if response.Data.Recorded != nil {
					properties["recorded"] = *response.Data.Recorded
					if eventType == "server_install_receipt" && !*response.Data.Recorded {
						result = "skipped"
						properties["reason"] = "not_recorded"
					}
				}
				if maintenanceOneOf(response.Data.State, "completed", "draft", "pending", "email_required", "in_progress") {
					properties["state"] = response.Data.State
				}
				if response.Data.Revision > 0 {
					properties["revision"] = response.Data.Revision
				}
			}
		} else {
			properties["reason"] = installHTTPFailureReason(status)
		}
		if eventType == "server_onboarding_step" {
			var request struct {
				Step int `json:"step"`
			}
			if json.Unmarshal(c.Request.Body(), &request) == nil && request.Step >= 2 && request.Step <= 5 {
				properties["step"] = request.Step
			}
		}
		if session, ok := c.Get("console_session_id"); ok {
			if sessionID, ok := session.(string); ok && sessionID != "" {
				properties["journey_id"] = hashString(sessionID)
			}
		}
		var actor *int64
		if id > 0 {
			actor = &id
		}
		sessionID, proof := string(c.GetHeader("X-Install-Session-ID")), string(c.GetHeader("X-Install-Session-Proof"))
		if !installSessionIDPattern.MatchString(sessionID) || !installProofPattern.MatchString(proof) {
			sessionID, proof = "", ""
		}
		// Ordinary recurring context reads are not onboarding requests. Observe
		// this endpoint only when the installation client supplies its session.
		if eventType == "server_context" && sessionID == "" {
			return
		}
		if actor == nil && sessionID == "" {
			return
		}
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if err := s.persistInstallServerObservation(writeCtx, actor, sessionID, proof, eventType, result, properties); err != nil {
			// Do not log SQL, proof, content, response snapshots or raw error text.
			reason := "unavailable"
			switch {
			case errors.Is(err, errInstallQuota):
				reason = "quota"
			case errors.Is(err, errInstallProof):
				reason = "invalid_proof"
			case errors.Is(err, errInstallBinding):
				reason = "binding_conflict"
			case errors.Is(err, errInstallExpired):
				reason = "expired"
			}
			logger.Ctx(ctx).Warn("install_lifecycle_observation_dropped", "event_type", eventType, "reason", reason)
		}
	}
}

func installHTTPFailureReason(status int) string {
	switch status {
	case 400, 422:
		return "validation"
	case 401, 403:
		return "unauthorized"
	case 409:
		return "conflict"
	case 429:
		return "rate_limited"
	default:
		return "unavailable"
	}
}

func (s *Service) persistInstallServerObservation(ctx context.Context, actor *int64, sessionID, proof, eventType, result string, properties map[string]interface{}) error {
	now := time.Now()
	return s.db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Default.LogMode(gormlogger.Silent)}).Transaction(func(tx *gorm.DB) error {
		var linkedSession interface{}
		if sessionID != "" {
			// Only an authenticated operation (or provision's verified successful
			// response) can bind an identity. Possession of an anonymous proof alone
			// never sets actor, grants a scope, or marks an Agent as completed.
			row, err := lockInstallLifecycleSession(tx, sessionID, proof, "", actor, now.UnixMilli(), false)
			if err != nil {
				return err
			}
			if row.EventCount >= 512 {
				return errInstallQuota
			}
			linkedSession = sessionID
		} else if actor != nil {
			// Authenticated business requests need not carry an installation
			// session. Bound their observations across replicas as well: a caller
			// must not grow a 180-day stream by repeatedly making invalid requests.
			day := now.UnixMilli() / int64(24*time.Hour/time.Millisecond)
			if err := consumeInstallQuota(tx, "server-agent:"+strconv.FormatInt(*actor, 10), day, 512); err != nil {
				return err
			}
		}
		propertiesJSON, err := json.Marshal(properties)
		if err != nil {
			return err
		}
		eventID := uuid.New().String()
		key := hashString("server:" + eventID)
		// A context read proves unlock, not sustained activity. Retain the first
		// outcome per session instead of recording every later heartbeat read.
		if eventType == "server_context" {
			key = hashString("server-context:" + sessionID + ":" + result)
		}
		insert := tx.Exec(`INSERT INTO install_lifecycle_events
			(event_key,event_id,install_session_id,agent_id,event_type,result,evidence,properties,payload_hash,event_at,received_at,expires_at)
			VALUES (?,?,?,?,?,?,'server_observed',?::jsonb,?,?,?,?) ON CONFLICT(event_key) DO NOTHING`, key, eventID,
			linkedSession, actor, eventType, result, string(propertiesJSON), hashString(string(propertiesJSON)),
			now.UnixMilli(), now.UnixMilli(), now.Add(installEventRetention).UnixMilli())
		if insert.Error != nil {
			return insert.Error
		}
		if linkedSession != nil && insert.RowsAffected > 0 {
			return tx.Exec(`UPDATE install_lifecycle_sessions SET event_count=event_count+1 WHERE install_session_id=?`, sessionID).Error
		}
		return nil
	})
}
