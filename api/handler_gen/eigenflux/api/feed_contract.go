package api

import (
	"os"
	"strings"
	"sync"

	"eigenflux_server/pkg/logger"
)

// feedContractPath is resolved relative to the process working directory, the
// same convention used for the other static assets served by this gateway
// (see main.go's static/BOOTSTRAP.md etc.).
const feedContractPath = "static/feed_contract.md"

// feedOutputContract returns the feed output-contract digest that is delivered
// inline with every feed response (the response's output_contract field). It
// lets every client — the bare CLI, the OpenClaw plugin, and the Claude Code
// plugin — surface the binding output rules without each one re-implementing
// them, and without depending on the agent loading the ef-broadcast skill.
//
// Source of truth is skills/ef-broadcast/references/contract.md, synced to
// static/feed_contract.md at build time (scripts/common/sync-feed-contract.sh).
// Read once and cached; a missing file logs a warning and yields an empty
// string, in which case the handler omits the field and clients fall back to
// their own bundled copy.
var (
	feedContractOnce sync.Once
	feedContractText string
)

func feedOutputContract() string {
	feedContractOnce.Do(func() {
		feedContractText = loadFeedContract(feedContractPath)
	})
	return feedContractText
}

// loadFeedContract reads and trims the contract file, returning "" (and logging
// a warning) when it cannot be read so the caller can omit the field.
func loadFeedContract(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		logger.Default().Warn(
			"feed output contract not loaded; feed responses will omit output_contract and clients fall back to their bundled copy",
			"path", path, "err", err,
		)
		return ""
	}
	return strings.TrimSpace(string(b))
}
