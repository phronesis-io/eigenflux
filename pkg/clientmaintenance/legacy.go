// Package clientmaintenance supplies bounded, server-owned migration guidance.
package clientmaintenance

import (
	"context"
	"regexp"
	"strings"

	"eigenflux_server/pkg/reqinfo"
)

// MinimumCLI is the unified automatic-maintenance bootstrap floor, not the
// compatibility floor of any particular Skills release.
const MinimumCLI = "1.0.0"

const InstallGuideURL = "https://cdn.eigenflux.ai/skills/latest/install.md"

const legacyInstructions = "This client predates the unified EigenFlux automatic-maintenance entrypoint. Follow the owner's maintenance policy. If upgrading is already authorized, read https://cdn.eigenflux.ai/skills/latest/install.md and upgrade the existing installation to the latest stable CLI and signed Skills. Preserve the same Agent Home, server, account, credentials, Skills target, enabled integrations and paused state; do not provision or switch accounts. Reuse the owned recurring trigger and run heartbeat plan --format agent with its existing explicit Home, server and runtime mode, then follow the freshly installed rules. A Feed poll or runtime heartbeat alone does not execute CLI maintenance. If owner action is required, notify once per unchanged blocker; otherwise keep routine maintenance quiet. Do not claim installation, loaded runtime or scheduler recovery without verification."

var legacyVersion = regexp.MustCompile(`^v?0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?(?:\+[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

// Attach is called only after Agent authentication and successful read handling.
// It neither changes business results nor infers that an absent version is old.
// Guidance stays outside network-authored items/messages and contains no input
// text, paths, account identifiers, commands interpolated from headers or URLs.
func Attach(ctx context.Context, data map[string]interface{}) {
	version := reqinfo.ClientFromContext(ctx).CLIVer
	if data == nil || len(version) > 32 || !legacyVersion.MatchString(version) {
		return
	}
	data["client_maintenance"] = map[string]interface{}{
		"schema_version":      "client_maintenance.v1",
		"reason":              "legacy_maintenance_entrypoint",
		"minimum_cli_version": MinimumCLI,
		"install_guide_url":   InstallGuideURL,
		"instructions":        legacyInstructions,
	}
	// Agent-format Feed renderers lift the server-owned output contract ahead of
	// the untrusted network payload. Empty pages must also carry the bridge.
	if contract, ok := data["output_contract"].(string); ok {
		data["output_contract"] = strings.TrimSpace(contract + "\n\nEIGENFLUX CLIENT MAINTENANCE\n" + legacyInstructions)
	}
}
