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

// ProfileRefreshTriggerHeader is sent by the CLI on refresh-complete and on
// refresh patches. It says what dispatched the refresh the CLI is finishing:
// "scheduled" (a host timer or the CLI's due reminder), "manual" (an explicit
// user request such as `profile refresh-task --force`) or "unknown".
const ProfileRefreshTriggerHeader = "X-EF-Profile-Refresh-Trigger"

// Change-event sources written by Periodic Profile Refresh. Scheduled runs
// keep the historical value so existing history stays comparable; the CLI
// rewrites the source of a manually requested refresh.
const (
	profileRefreshSourceScheduled = "cli_daily_refresh"
	profileRefreshSourceManual    = "cli_manual_refresh"
)

const (
	refreshTriggerScheduled = "scheduled"
	refreshTriggerManual    = "manual"
	refreshTriggerUnknown   = "unknown"
)

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

// isProfileRefreshSource reports whether a profile-fields write finishes a
// Periodic Profile Refresh run.
func isProfileRefreshSource(source string) bool {
	return source == profileRefreshSourceScheduled || source == profileRefreshSourceManual
}

// normalizeRefreshTrigger bounds the client-supplied trigger to the known
// enum. Old CLIs send no header and are reported as unknown; nothing is ever
// rejected because of this header.
func normalizeRefreshTrigger(header string) string {
	switch strings.ToLower(strings.TrimSpace(header)) {
	case refreshTriggerScheduled:
		return refreshTriggerScheduled
	case refreshTriggerManual:
		return refreshTriggerManual
	default:
		return refreshTriggerUnknown
	}
}

// refreshTriggerFor resolves the raw trigger for one logged run. A
// manual-refresh source is authoritative; otherwise the header decides.
// logProfileRefreshRun normalizes the result.
func refreshTriggerFor(source, header string) string {
	if source == profileRefreshSourceManual {
		return refreshTriggerManual
	}
	return header
}

// logProfileRefreshRun emits one structured line per finished refresh
// evaluation. trigger is the raw client value and is bounded here. Only
// bounded enum and version values are logged, never raw host strings or
// request bodies.
func logProfileRefreshRun(ctx context.Context, c *app.RequestContext, agentID int64, outcome, trigger string) {
	headers := reqinfo.BoundedClientHeaders(func(name string) string { return string(c.GetHeader(name)) })
	identity, _ := runtimeidentity.Parse(headers.Host)
	logger.Ctx(ctx).Info(profileRefreshRunLogMsg,
		"agent_id", agentID,
		"outcome", outcome,
		"trigger", normalizeRefreshTrigger(trigger),
		"mode", headers.Mode,
		"runtime_name", identity.Name,
		"cli_version", reqinfo.SafePluginVersion(headers.CLIVersion),
		"plugin_version", headers.PluginVersion,
	)
}
