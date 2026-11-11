// Package feedcontract owns the binding safety/output contract included in
// every EigenFlux Feed response. Contracts are generated from the central
// Skills and selected by mode, without host-specific business rules.
package feedcontract

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"eigenflux_server/pkg/logger"
	"github.com/coreos/go-semver/semver"
)

const DefaultPath = "static/feed_contract.md"

var (
	defaultOnce  sync.Once
	defaultText  string
	baselineOnce sync.Once
	baselineText string
)

// ForMode selects the contract generated from the corresponding dynamic Skill.
func ForMode(mode string) string {
	if mode != "baseline" {
		return Default()
	}
	baselineOnce.Do(func() { baselineText = Load("static/feed_baseline_contract.md") })
	return baselineText
}

// Default returns the repository-generated contract, read once per process.
func Default() string {
	defaultOnce.Do(func() {
		defaultText = Load(DefaultPath)
	})
	return defaultText
}

// Load reads and trims a contract file. Missing files fail soft so clients can
// use their synchronized Skills, while the server emits an actionable warning.
func Load(path string) string {
	body, err := os.ReadFile(path)
	if err != nil && !filepath.IsAbs(path) {
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
				candidate := filepath.Join(dir, path)
				if body, err = os.ReadFile(candidate); err == nil {
					break
				}
				parent := filepath.Dir(dir)
				if parent == dir {
					break
				}
			}
		}
	}
	if err != nil {
		logger.Default().Warn(
			"feed output contract not loaded; clients must resolve the current synchronized Skill",
			"path", path, "err", err,
		)
		return ""
	}
	return strings.TrimSpace(string(body))
}

// ForClient preserves the contract except for commands absent from this CLI.
// pending-confirmations first shipped with CLI 1.0.16 (fabf6ef0).
func ForClient(mode, cliVersion string) string {
	contract := ForMode(mode)
	version, err := semver.NewVersion(strings.TrimPrefix(strings.TrimSpace(cliVersion), "v"))
	if mode == "baseline" || (err == nil && !version.LessThan(*semver.New("1.0.16"))) {
		return contract
	}
	lines := strings.Split(contract, "\n")
	for i, line := range lines {
		if strings.Contains(line, "eigenflux order pending-confirmations") {
			lines[i] = "- **Pending Orders compatibility:** This CLI has no verified support for the pending Order check. Continue Feed scoring and delivery. Report the check as unavailable; never claim there are no pending Orders or confirm receipt automatically. Upgrade the CLI before checking pending Orders."
		}
	}
	return strings.Join(lines, "\n")
}
