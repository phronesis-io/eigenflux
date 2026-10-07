package agentcardapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/reqinfo"
	profiledal "eigenflux_server/rpc/profile/dal"
)

const (
	maxRefreshRunBodyBytes = 4 << 10
	// Only new rows are charged: hourly re-reminders reuse their run_id and
	// retries repeat (run_id, stage), so both are answered as duplicates
	// before the quota. A cooperative CLI adds a few rows per day (one run per
	// 24h freshness window, manual reviews, and their completions); the
	// rolling cap bounds a misbehaving or hostile client minting new run_ids.
	refreshRunDailyLimit  = 60
	refreshRunDailyWindow = 24 * time.Hour
)

var refreshRunIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

var refreshRunTriggers = map[string]bool{
	"plugin_task":  true,
	"pending_line": true,
	"manual_force": true,
	"untracked":    true,
}

// RefreshRunReq is one Periodic Profile Refresh health report.
type RefreshRunReq struct {
	RunID        string   `json:"run_id"`
	Stage        string   `json:"stage"`
	Outcome      string   `json:"outcome,omitempty"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	Trigger      string   `json:"trigger"`
}

// validate normalizes the request and returns the JSONB changed_paths text
// for completed/changed runs.
func (r *RefreshRunReq) validate() (*string, error) {
	if !refreshRunIDPattern.MatchString(r.RunID) {
		return nil, fmt.Errorf("run_id must be 8-64 characters of [A-Za-z0-9_-]")
	}
	if !refreshRunTriggers[r.Trigger] {
		return nil, fmt.Errorf("trigger must be plugin_task, pending_line, manual_force, or untracked")
	}
	switch r.Stage {
	case "dispatched":
		if r.Outcome != "" || len(r.ChangedPaths) > 0 {
			return nil, fmt.Errorf("dispatched runs must not carry outcome or changed_paths")
		}
		return nil, nil
	case "completed":
	default:
		return nil, fmt.Errorf("stage must be dispatched or completed")
	}
	switch r.Outcome {
	case "unchanged":
		if len(r.ChangedPaths) > 0 {
			return nil, fmt.Errorf("unchanged runs must not carry changed_paths")
		}
		empty := "[]"
		return &empty, nil
	case "changed":
	default:
		return nil, fmt.Errorf("completed runs require outcome changed or unchanged")
	}
	if len(r.ChangedPaths) == 0 || len(r.ChangedPaths) > maxFieldsPerUpdate {
		return nil, fmt.Errorf("changed runs require 1-%d changed_paths", maxFieldsPerUpdate)
	}
	seen := make(map[string]bool, len(r.ChangedPaths))
	paths := make([]string, 0, len(r.ChangedPaths))
	for _, path := range r.ChangedPaths {
		if _, ok := agentcard.LookupField(path); !ok {
			return nil, fmt.Errorf("unknown changed path %q", path)
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	encoded, err := json.Marshal(paths)
	if err != nil {
		return nil, err
	}
	text := string(encoded)
	return &text, nil
}

// PostRefreshRun records one dispatched or completed Periodic Profile Refresh
// run. Reports are idempotent on (agent, run_id, stage); a repeat is answered
// as a duplicate without charging the daily quota. Client metadata uses the
// same headers and bounds as runtime observation (reqinfo.BoundedClientHeaders).
// @Summary Record a Periodic Profile Refresh run event
// @Tags Agent Card
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body RefreshRunReq true "Run event"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/agents/me/card/refresh-runs [post]
func PostRefreshRun(ctx context.Context, c *app.RequestContext) {
	agentID, ok := callerAgentID(c)
	if !ok {
		return
	}
	body, err := c.Body()
	if err != nil {
		respond(c, http.StatusBadRequest, 400, "failed to read request body", nil)
		return
	}
	if len(body) > maxRefreshRunBodyBytes {
		respond(c, http.StatusRequestEntityTooLarge, 413, fmt.Sprintf("refresh run body exceeds %d bytes", maxRefreshRunBodyBytes), nil)
		return
	}
	var req RefreshRunReq
	if err := json.Unmarshal(body, &req); err != nil {
		respond(c, http.StatusBadRequest, 400, "invalid JSON body", nil)
		return
	}
	changedPaths, verr := req.validate()
	if verr != nil {
		respond(c, http.StatusBadRequest, 400, verr.Error(), nil)
		return
	}
	// Retries and hourly re-reminders repeat an already recorded (run_id,
	// stage). Answer them from the unique index without charging the quota.
	exists, err := profiledal.ProfileRefreshRunExists(db.DB.WithContext(ctx), agentID, req.RunID, req.Stage)
	if err != nil {
		logger.Ctx(ctx).Error("PostRefreshRun lookup failed", "agentID", agentID, "stage", req.Stage, "err", err)
		respond(c, http.StatusInternalServerError, 500, "failed to record refresh run", nil)
		return
	}
	if exists {
		respondRefreshRunRecorded(c, false)
		return
	}
	// Fail closed like profile writes: an unbounded telemetry endpoint would
	// grow the table without limit while Redis is unavailable.
	allowed, rateErr := checkFixedWindow(ctx, mq.RDB, agentID, "refresh-runs", refreshRunDailyLimit, refreshRunDailyWindow, time.Now())
	if rateErr != nil {
		logger.Ctx(ctx).Error("refresh-runs rate limiter unavailable, failing closed", "agentID", agentID, "err", rateErr)
		c.Header("Retry-After", "60")
		respond(c, http.StatusServiceUnavailable, 503, "refresh run reporting is temporarily unavailable", nil)
		return
	}
	if !allowed {
		c.Header("Retry-After", "3600")
		respond(c, http.StatusTooManyRequests, 429, "too many refresh run reports, slow down", nil)
		return
	}

	headers := reqinfo.BoundedClientHeaders(func(name string) string { return string(c.GetHeader(name)) })
	run := &profiledal.ProfileRefreshRun{
		AgentID:       agentID,
		RunID:         req.RunID,
		Stage:         req.Stage,
		ChangedPaths:  changedPaths,
		Trigger:       req.Trigger,
		ClientHost:    headers.Host,
		ClientMode:    headers.Mode,
		CLIVersion:    headers.CLIVersion,
		PluginVersion: headers.PluginVersion,
	}
	if req.Stage == "completed" {
		outcome := req.Outcome
		run.Outcome = &outcome
	}
	inserted, err := profiledal.InsertProfileRefreshRun(db.DB.WithContext(ctx), run)
	if err != nil {
		logger.Ctx(ctx).Error("PostRefreshRun insert failed", "agentID", agentID, "stage", req.Stage, "err", err)
		respond(c, http.StatusInternalServerError, 500, "failed to record refresh run", nil)
		return
	}
	respondRefreshRunRecorded(c, inserted)
}

func respondRefreshRunRecorded(c *app.RequestContext, inserted bool) {
	respond(c, http.StatusOK, 0, "success", map[string]interface{}{
		"recorded":  inserted,
		"duplicate": !inserted,
	})
}
