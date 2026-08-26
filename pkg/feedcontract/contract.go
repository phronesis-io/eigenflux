// Package feedcontract owns the binding safety/output contract included in
// every EigenFlux Feed response. Both V1 and V2 use this single cached source
// so host plugins never need to invent or periodically synchronize the rules.
package feedcontract

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"eigenflux_server/pkg/logger"
)

const DefaultPath = "static/feed_contract.md"

var (
	defaultOnce sync.Once
	defaultText string
)

// Default returns the repository-generated contract, read once per process.
func Default() string {
	defaultOnce.Do(func() {
		defaultText = Load(DefaultPath)
	})
	return defaultText
}

// Load reads and trims a contract file. Missing files fail soft so clients can
// use their bundled copy, while the server emits an actionable warning.
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
			"feed output contract not loaded; clients will use their bundled copy",
			"path", path, "err", err,
		)
		return ""
	}
	return strings.TrimSpace(string(body))
}
