package agentcardapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"github.com/cloudwego/hertz/pkg/app"
)

const ProfileRefreshRunHeader = "X-EF-Profile-Refresh-Run"

var refreshRunIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func safeRefreshRunID(value string) string {
	if refreshRunIDPattern.MatchString(value) {
		return value
	}
	return ""
}

type refreshFunnelEvent struct {
	Stage       string `json:"stage"`
	RunID       string `json:"run_id,omitempty"`
	PollID      string `json:"poll_id,omitempty"`
	Cause       string `json:"cause"`
	FailureCode string `json:"failure_code,omitempty"`
}

func validRefreshFunnelEvent(e refreshFunnelEvent) bool {
	if e.Cause != "feed_poll" && e.Cause != "host_schedule" && e.Cause != "manual" {
		return false
	}
	if e.Cause == "feed_poll" && e.PollID == "" {
		return false
	}
	if e.Cause != "feed_poll" && e.PollID != "" {
		return false
	}
	if e.RunID != "" && safeRefreshRunID(e.RunID) == "" || e.PollID != "" && safeRefreshRunID(e.PollID) == "" {
		return false
	}
	switch e.Stage {
	case "poll_started", "poll_succeeded":
		return e.Cause == "feed_poll" && e.PollID != "" && e.RunID == "" && e.FailureCode == ""
	case "poll_failed":
		return e.Cause == "feed_poll" && e.PollID != "" && e.RunID == "" && validRefreshFailureCode(e.FailureCode)
	case "refresh_dispatched":
		return e.RunID != "" && e.FailureCode == ""
	case "refresh_attempt_failed":
		return e.RunID != "" && validRefreshFailureCode(e.FailureCode)
	default:
		return false
	}
}
func validRefreshFailureCode(code string) bool {
	return code == "version_conflict" || code == "api_error" || code == "client_error"
}

// RecordRefreshEvent accepts only bounded lifecycle metadata. Auth middleware
// determines the Agent; client-selected identities and arbitrary text are rejected.
func RecordRefreshEvent(ctx context.Context, c *app.RequestContext) {
	agentID, ok := callerAgentID(c)
	if !ok {
		return
	}
	if len(c.Request.Body()) > 2048 {
		respond(c, http.StatusBadRequest, 400, "refresh event too large", nil)
		return
	}
	var event refreshFunnelEvent
	decoder := json.NewDecoder(bytes.NewReader(c.Request.Body()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&event) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validRefreshFunnelEvent(event) {
		respond(c, http.StatusBadRequest, 400, "invalid refresh event", nil)
		return
	}
	allowed, err := checkFixedWindow(ctx, mq.RDB, agentID, "refresh-events", 120, time.Minute, time.Now())
	if err != nil {
		respond(c, http.StatusServiceUnavailable, 503, "refresh telemetry temporarily unavailable", nil)
		return
	}
	if !allowed {
		respond(c, http.StatusTooManyRequests, 429, "too many refresh events", nil)
		return
	}
	logger.Ctx(ctx).Info("agent_profile_refresh_funnel", "agent_id", agentID, "stage", event.Stage, "run_id", event.RunID, "poll_id", event.PollID, "cause", event.Cause, "failure_code", event.FailureCode)
	respond(c, http.StatusOK, 0, "success", nil)
}
