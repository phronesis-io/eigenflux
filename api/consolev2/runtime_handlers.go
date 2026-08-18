package consolev2

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
)

const runtimeLeaseTTL = 90 * time.Second

type runtimeHeartbeatRequest struct {
	RuntimeInstanceID      string   `json:"runtime_instance_id"`
	Capabilities           []string `json:"capabilities"`
	SessionRef             *string  `json:"session_ref,omitempty"`
	AppliedContextRevision *int64   `json:"applied_context_revision,omitempty"`
}

func normalizeCapabilities(values []string) ([]string, bool) {
	if len(values) > 32 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 64 {
			return nil, false
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, true
}

func (s *Service) runtimeHeartbeat(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	var req runtimeHeartbeatRequest
	if decodeBody(c, &req) != nil || req.RuntimeInstanceID == "" || len(req.RuntimeInstanceID) > 128 ||
		(req.SessionRef != nil && len(*req.SessionRef) > 512) ||
		(req.AppliedContextRevision != nil && *req.AppliedContextRevision <= 0) {
		fail(c, http.StatusBadRequest, "INVALID_RUNTIME_HEARTBEAT", "runtime heartbeat is invalid", nil)
		return
	}
	capabilities, valid := normalizeCapabilities(req.Capabilities)
	if !valid {
		fail(c, http.StatusBadRequest, "INVALID_CAPABILITIES", "runtime capabilities are invalid", nil)
		return
	}
	var lease struct {
		LeaseUntil int64 `gorm:"column:lease_until"`
		Now        int64 `gorm:"column:now_ms"`
	}
	result := s.db.Raw(`WITH clock AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS now_ms
		) INSERT INTO agent_runtime_leases
		(agent_id, runtime_instance_id, capabilities, session_ref, context_revision_applied,
		 lease_until, last_heartbeat_at, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, clock.now_ms + ?, clock.now_ms, clock.now_ms, clock.now_ms FROM clock
		ON CONFLICT (agent_id, runtime_instance_id) DO UPDATE SET
		 capabilities = EXCLUDED.capabilities, session_ref = EXCLUDED.session_ref,
		 context_revision_applied = EXCLUDED.context_revision_applied,
		 lease_until = EXCLUDED.lease_until, last_heartbeat_at = EXCLUDED.last_heartbeat_at,
		 updated_at = EXCLUDED.updated_at
		RETURNING lease_until,
			(extract(epoch FROM clock_timestamp())*1000)::bigint AS now_ms`, agentIDValue,
		req.RuntimeInstanceID, pq.Array(capabilities), req.SessionRef, req.AppliedContextRevision,
		int64(runtimeLeaseTTL/time.Millisecond)).Scan(&lease)
	if result.Error != nil {
		if isForeignKeyViolation(result.Error) {
			fail(c, http.StatusConflict, "CONTEXT_REQUIRED", "applied context revision does not exist", nil)
			return
		}
		fail(c, http.StatusInternalServerError, "RUNTIME_HEARTBEAT_FAILED", "could not renew runtime lease", nil)
		return
	}
	commandIDs, err := s.pendingCommandIDs(agentIDValue, lease.Now, 50)
	if err != nil {
		fail(c, http.StatusInternalServerError, "RUNTIME_RECONCILE_FAILED", "could not reconcile pending commands", nil)
		return
	}
	ids := make([]string, 0, len(commandIDs))
	for _, id := range commandIDs {
		ids = append(ids, fmt.Sprintf("%d", id))
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"runtime_instance_id": req.RuntimeInstanceID, "lease_until": lease.LeaseUntil,
		"pending_command_ids": ids, "next_heartbeat_seconds": 30,
	})
}
