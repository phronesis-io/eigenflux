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
