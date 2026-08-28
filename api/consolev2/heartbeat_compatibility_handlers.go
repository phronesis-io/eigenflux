package consolev2

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	consoledal "eigenflux_server/api/dal"
	"eigenflux_server/pkg/reqinfo"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"
)

const heartbeatContractV1 = "eigenflux_heartbeat.v1"

var skillRevisionPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type heartbeatCompatibilityReport struct {
	ContractVersion string `json:"heartbeat_contract_version"`
	SkillRevision   string `json:"skill_revision"`
}

type heartbeatCompatibilityState struct {
	CLIVersion        string `gorm:"column:cli_version"`
	HeartbeatContract string `gorm:"column:heartbeat_contract_version"`
	SkillRevision     string `gorm:"column:skill_revision"`
	ReportedAt        int64  `gorm:"column:heartbeat_reported_at"`
}

func (s *Service) reportHeartbeatCompatibility(ctx context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	var req heartbeatCompatibilityReport
	clientInfo := reqinfo.ClientFromContext(ctx)
	if decodeBody(c, &req) != nil || strings.TrimSpace(clientInfo.CLIVer) == "" ||
		req.ContractVersion != heartbeatContractV1 || !skillRevisionPattern.MatchString(req.SkillRevision) {
		fail(c, http.StatusBadRequest, "INVALID_HEARTBEAT_REPORT", "heartbeat compatibility report is invalid", nil)
		return
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return consoledal.UpdateHeartbeatCompatibility(tx, agentIDValue, clientInfo.CLIVer, req.ContractVersion, req.SkillRevision)
	}); err != nil {
		fail(c, http.StatusInternalServerError, "HEARTBEAT_REPORT_FAILED", "could not save heartbeat compatibility report", nil)
		return
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"heartbeat_contract_version": req.ContractVersion,
		"skill_revision":             req.SkillRevision,
		"cli_version":                clientInfo.CLIVer,
	})
}

func (s *Service) loadConsoleV2Compatibility(agentID int64) (map[string]interface{}, error) {
	var state heartbeatCompatibilityState
	err := s.db.Raw(`SELECT
		COALESCE(cli_version, '') AS cli_version,
		COALESCE(heartbeat_contract_version, '') AS heartbeat_contract_version,
		COALESCE(skill_revision, '') AS skill_revision,
		COALESCE(heartbeat_reported_at, 0) AS heartbeat_reported_at
		FROM agent_settings WHERE agent_id = ?`, agentID).Scan(&state).Error
	if err != nil {
		return nil, err
	}
	return consoleV2Compatibility(
		state.CLIVersion,
		state.HeartbeatContract,
		state.SkillRevision,
		state.ReportedAt,
	), nil
}

// LegacyConsoleCompatibilityHandler exposes the additive Console V2 runtime
// status to an existing V1 bearer session. It is deliberately read-only: old
// Console bundles never call this route, and no V1 API is gated by its result.
func (s *Service) LegacyConsoleCompatibilityHandler() app.HandlerFunc {
	return func(_ context.Context, c *app.RequestContext) {
		agentIDValue, ok := agentID(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"code": 401, "msg": "missing authenticated Agent", "data": nil,
			})
			return
		}
		compatibility, err := s.loadConsoleV2Compatibility(agentIDValue)
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"code": 500, "msg": "could not read Console V2 compatibility", "data": nil,
			})
			return
		}
		c.Header("Cache-Control", "private, no-store")
		c.JSON(http.StatusOK, map[string]interface{}{
			"code": 0, "msg": "success", "data": map[string]interface{}{
				"compatibility": compatibility,
			},
		})
	}
}
