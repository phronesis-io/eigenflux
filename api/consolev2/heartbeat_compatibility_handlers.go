package consolev2

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	consoledal "eigenflux_server/api/dal"
	"eigenflux_server/pkg/reqinfo"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"
)

const (
	heartbeatContractV1 = "eigenflux_heartbeat.v1"
	minimumConsoleV2CLI = "0.0.34"
)

var skillRevisionPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var heartbeatContractPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type heartbeatCompatibilityReport struct {
	ContractVersion string `json:"heartbeat_contract_version"`
	SkillRevision   string `json:"skill_revision"`
}

type heartbeatCompatibilityState struct {
	CLIVersion        string `gorm:"column:cli_version"`
	HeartbeatContract string `gorm:"column:heartbeat_contract_version"`
	SkillRevision     string `gorm:"column:skill_revision"`
	ReportedAt        int64  `gorm:"column:heartbeat_reported_at"`
	OnboardingState   string `gorm:"column:onboarding_state"`
}

const loadConsoleV2CompatibilityQuery = `SELECT
		COALESCE(settings.cli_version, '') AS cli_version,
		COALESCE(settings.heartbeat_contract_version, '') AS heartbeat_contract_version,
		COALESCE(settings.skill_revision, '') AS skill_revision,
		COALESCE(settings.heartbeat_reported_at, 0) AS heartbeat_reported_at,
		COALESCE(onboarding.state, 'not_started') AS onboarding_state
		FROM (SELECT CAST(? AS BIGINT) AS agent_id) current_agent
		LEFT JOIN agent_settings settings ON settings.agent_id = current_agent.agent_id
		LEFT JOIN agent_onboarding_v2 onboarding ON onboarding.agent_id = current_agent.agent_id`

func (s *Service) reportHeartbeatCompatibility(ctx context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	var req heartbeatCompatibilityReport
	clientInfo := reqinfo.ClientFromContext(ctx)
	if decodeBody(c, &req) != nil || strings.TrimSpace(clientInfo.CLIVer) == "" ||
		!heartbeatContractPattern.MatchString(req.ContractVersion) || !skillRevisionPattern.MatchString(req.SkillRevision) {
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

func (s *Service) loadConsoleV2Compatibility(agentIDValue int64) (map[string]interface{}, string, error) {
	var state heartbeatCompatibilityState
	err := s.db.Raw(loadConsoleV2CompatibilityQuery, agentIDValue).Scan(&state).Error
	if err != nil {
		return nil, "", err
	}
	return consoleV2Compatibility(
		state.CLIVersion,
		state.HeartbeatContract,
		state.SkillRevision,
		state.ReportedAt,
		state.OnboardingState == "completed",
	), state.OnboardingState, nil
}

// LegacyConsoleCompatibilityHandler is an additive, read-only endpoint for a
// V1 bearer session. It never changes or gates any existing V1 API.
func (s *Service) LegacyConsoleCompatibilityHandler() app.HandlerFunc {
	return func(_ context.Context, c *app.RequestContext) {
		agentIDValue, ok := agentID(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"code": 401, "msg": "missing authenticated Agent", "data": nil,
			})
			return
		}
		compatibility, onboardingState, err := s.loadConsoleV2Compatibility(agentIDValue)
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
				"onboarding":    map[string]interface{}{"state": onboardingState},
			},
		})
	}
}

func consoleV2Compatibility(cliVersion, heartbeatContract, skillRevision string, reportedAt int64, onboardingCompleted bool) map[string]interface{} {
	status := "ready"
	reason := ""
	available := true
	if !onboardingCompleted {
		switch {
		case strings.TrimSpace(cliVersion) == "":
			status, reason, available = "unknown", "report_missing", false
		case compareConsoleCLIVersion(cliVersion, minimumConsoleV2CLI) < 0:
			status, reason, available = "upgrade_required", "cli_outdated", false
		}
	}
	return map[string]interface{}{
		"available":                           available,
		"status":                              status,
		"reason":                              reason,
		"cli_version":                         cliVersion,
		"minimum_cli_version":                 minimumConsoleV2CLI,
		"heartbeat_contract_version":          heartbeatContract,
		"required_heartbeat_contract_version": heartbeatContractV1,
		"skill_revision":                      skillRevision,
		"reported_at":                         reportedAt,
	}
}

func compareConsoleCLIVersion(left, right string) int {
	parse := func(value string) [3]int {
		var result [3]int
		value = strings.TrimPrefix(strings.TrimSpace(value), "v")
		if cut := strings.IndexAny(value, "-+"); cut >= 0 {
			value = value[:cut]
		}
		parts := strings.Split(value, ".")
		for index := 0; index < len(parts) && index < len(result); index++ {
			result[index], _ = strconv.Atoi(parts[index])
		}
		return result
	}
	a, b := parse(left), parse(right)
	for index := range a {
		if a[index] < b[index] {
			return -1
		}
		if a[index] > b[index] {
			return 1
		}
	}
	return 0
}
