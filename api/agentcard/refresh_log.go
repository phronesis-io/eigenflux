package agentcardapi

import (
	"context"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/reqinfo"
	"eigenflux_server/pkg/runtimeidentity"
)

// ProfileRefreshCompleteHeader is sent by `eigenflux profile refresh-complete`
// on its refresh-context version check. Its value is the profile_version the
// Agent evaluated.
const ProfileRefreshCompleteHeader = "X-EF-Profile-Refresh-Complete"

// profileRefreshRunLogMsg is the Loki message for one finished Periodic
// Profile Refresh evaluation; see docs/dev/api_endpoints.md.
const profileRefreshRunLogMsg = "agent_profile_refresh_run"

// refreshCompleteOutcome maps the refresh-complete header to an outcome.
// "unchanged" means the CLI will record the no-change evaluation; "stale"
// means the profile moved since evaluation and the CLI rejects the completion.
// A missing or malformed header is not a refresh completion.
func refreshCompleteOutcome(header string, currentVersion int64) (string, bool) {
	expected, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64)
	if err != nil {
		return "", false
	}
	if expected != currentVersion {
		return "stale", true
	}
	return "unchanged", true
}

// logProfileRefreshRun emits one structured line per finished refresh
// evaluation. Only bounded enum and version values are logged, never raw
// host strings or request bodies.
func logProfileRefreshRun(ctx context.Context, c *app.RequestContext, agentID int64, outcome string) {
	headers := reqinfo.BoundedClientHeaders(func(name string) string { return string(c.GetHeader(name)) })
	identity, _ := runtimeidentity.Parse(headers.Host)
	logger.Ctx(ctx).Info(profileRefreshRunLogMsg,
		"agent_id", agentID,
		"outcome", outcome,
		"mode", headers.Mode,
		"runtime_name", identity.Name,
		"cli_version", reqinfo.SafePluginVersion(headers.CLIVersion),
		"plugin_version", headers.PluginVersion,
	)
}
