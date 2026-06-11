package api

import (
	"os"
	"strings"
	"sync"
)

// feedOutputContract returns the feed output-contract digest that is delivered
// inline with every feed response (the response's output_contract field). It
// lets every client — the bare CLI, the OpenClaw plugin, and the Claude Code
// plugin — surface the binding output rules without each one re-implementing
// them, and without depending on the agent loading the ef-broadcast skill.
//
// Source of truth is skills/ef-broadcast/references/contract.md, synced to
// static/feed_contract.md at build time (scripts/common/sync-feed-contract.sh).
// Read once and cached; a missing file yields an empty string, in which case
// clients fall back to their own bundled copy.
var (
	feedContractOnce sync.Once
	feedContractText string
)

func feedOutputContract() string {
	feedContractOnce.Do(func() {
		if b, err := os.ReadFile("static/feed_contract.md"); err == nil {
			feedContractText = strings.TrimSpace(string(b))
		}
	})
	return feedContractText
}
