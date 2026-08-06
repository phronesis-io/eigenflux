// Package runtimeidentity parses the self-reported host identity carried by
// EigenFlux CLI requests. It deliberately keeps product identity independent
// from the integration mode (plugin, skill, or CLI-direct).
package runtimeidentity

import "strings"

const maxPartLen = 64

// Identity is a normalized, self-reported Agent product identity.
type Identity struct {
	Name     string
	Version  string
	IsPlugin bool
}

// Parse accepts "name" or "name/version". "terminal" is the CLI transport
// fallback rather than an Agent product and therefore produces no identity.
func Parse(raw string) (Identity, bool) {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" || host == "terminal" {
		return Identity{}, false
	}
	parts := strings.SplitN(host, "/", 2)
	if !validPart(parts[0]) {
		return Identity{}, false
	}
	identity := Identity{Name: parts[0], IsPlugin: isPluginName(parts[0])}
	if len(parts) == 2 {
		if !validPart(parts[1]) {
			return Identity{}, false
		}
		identity.Version = parts[1]
	}
	return identity, true
}

func validPart(value string) bool {
	if value == "" || len(value) > maxPartLen {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return !(r == '.' || r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z')
	}) < 0
}

func isPluginName(name string) bool {
	switch name {
	case "openclaw", "claude-code", "codex":
		return true
	default:
		return false
	}
}
