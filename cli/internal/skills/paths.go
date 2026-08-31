package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cli.eigenflux.ai/internal/config"
)

// ResolveSkillsDir resolves the host's real skill-load directory.
//
// gate-4 (verified 2026-06-30): openclaw, codex, and bare terminal all load
// from ~/.agents/skills (OpenClaw precedence 3, higher than ~/.openclaw/skills;
// Codex USER scope = $HOME/.agents/skills per official docs). Only Claude Code
// loads from ~/.claude/skills. We deliberately do NOT reuse config.HomeDir():
// it force-appends a ".eigenflux" suffix (config.ensureEigenfluxSuffix), which
// is correct for the data home but wrong for a skills dir.
//
// Precedence: --into > EIGENFLUX_SKILLS_DIR > registered target > --host >
// autodetect > ~/.agents/skills. The registration is scoped to the resolved
// EigenFlux Home and never inferred by scanning directories.
func ResolveSkillsDir(into, host string) (string, error) {
	if into != "" {
		return into, nil
	}
	if d := os.Getenv("EIGENFLUX_SKILLS_DIR"); d != "" {
		return d, nil
	}
	if target, err := ReadTargetRegistration(config.HomeDir()); err != nil {
		return "", err
	} else if target != nil {
		return target.Path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	if host == "" {
		host = autodetectHost()
	}
	host = strings.ToLower(strings.TrimSpace(strings.SplitN(host, "/", 2)[0]))
	switch host {
	case "claude-code":
		return filepath.Join(home, ".claude", "skills"), nil
	case "openclaw", "codex", "workbuddy", "hermes", "terminal", "":
		return filepath.Join(home, ".agents", "skills"), nil
	default:
		return "", fmt.Errorf("unknown host %q (register it with skills target set)", host)
	}
}

// autodetectHost derives the host from the EIGENFLUX_HOST env var (e.g.
// "claude-code/0.0.5" -> "claude-code"). Only claude-code needs special routing;
// everything else shares ~/.agents/skills, so an unknown/empty host is harmless.
func autodetectHost() string {
	h := os.Getenv("EIGENFLUX_HOST")
	if h == "" {
		return "terminal"
	}
	// "claude-code/0.0.5" -> "claude-code"
	prefix := h
	if i := strings.IndexByte(h, '/'); i >= 0 {
		prefix = h[:i]
	}
	switch prefix {
	case "claude-code":
		return "claude-code"
	case "openclaw", "codex", "workbuddy", "hermes":
		return prefix
	default:
		return "terminal"
	}
}
