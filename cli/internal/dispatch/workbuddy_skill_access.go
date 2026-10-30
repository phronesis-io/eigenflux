package dispatch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"cli.eigenflux.ai/internal/skills"
)

var ErrCommissionSkillReadScopeInvalid = errors.New("commission_skill_read_scope_invalid")

// Grant only this invocation's resolved Skill to the current CLI's own bridge.
// Explicit third-party ACP commands retain their existing permission contract.
func (r Runner) withReadOnlySkillAccess(req Request) (Runner, error) {
	r = r.withCurrentWorkBuddyBridge()
	if len(req.ReadOnlySkillDirs) == 0 || r.Binding.Host != "workbuddy" || r.Binding.Mode != "acp" {
		return r, nil
	}
	switch req.Kind {
	case "commission_order", "commission_fulfillment", "commission_publish_trial":
	default:
		return r, nil
	}
	command := r.Binding.Command
	if len(command) < 3 || command[1] != "watch" || command[2] != "workbuddy-acp" || !filepath.IsAbs(command[0]) {
		return r, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return r, ErrNeedsUser
	}
	current, err := os.Stat(exe)
	if err != nil {
		return r, ErrNeedsUser
	}
	selected, err := os.Stat(command[0])
	if err != nil || !os.SameFile(current, selected) {
		return r, nil
	}
	invalid := errors.Join(ErrNeedsUser, ErrCommissionSkillReadScopeInvalid)
	if len(req.ReadOnlySkillDirs) != 1 {
		return r, invalid
	}
	for _, arg := range append(append([]string{}, command[3:]...), r.Binding.Args...) {
		if arg == "--" {
			return r, invalid
		}
	}
	dir := req.ReadOnlySkillDirs[0]
	root := r.Binding.SkillsDir
	if !safeWorkBuddySkillPath(root) || !safeWorkBuddySkillPath(dir) {
		return r, invalid
	}
	if name, err := filepath.Rel(root, dir); err != nil || !literalCommissionSkillName(name) {
		// The contract loader also resolves a host Skill outside the signed
		// EigenFlux bundle. Admit that same account-scoped registered/default
		// root, never an arbitrary parent supplied by a prompt or another Home.
		root, err = skills.ResolveSkillsDirForHome(r.Binding.Home, "", r.Binding.Host)
		if err != nil || !safeWorkBuddySkillPath(root) {
			return r, invalid
		}
	}
	for _, path := range []string{root, dir, filepath.Join(dir, "SKILL.md")} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (path == filepath.Join(dir, "SKILL.md") && !info.Mode().IsRegular()) || (path != filepath.Join(dir, "SKILL.md") && !info.IsDir()) {
			return r, invalid
		}
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return r, invalid
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil || !safeWorkBuddySkillPath(canonicalRoot) || !safeWorkBuddySkillPath(dir) {
		return r, invalid
	}
	name, err := filepath.Rel(canonicalRoot, dir)
	if err != nil || !literalCommissionSkillName(name) {
		return r, invalid
	}
	count := 0
	err = filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		count++
		if err != nil || count > 4096 || entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return invalid
		}
		return nil
	})
	if err != nil {
		return r, invalid
	}
	rule, err := workBuddySkillReadRule(dir, runtime.GOOS)
	if err != nil {
		return r, invalid
	}
	// Never mutate the persisted binding or reuse another invocation's grants.
	r.Binding.Args = append(append([]string{}, r.Binding.Args...), "--allow-tool", rule)
	return r, nil
}

func safeWorkBuddySkillPath(value string) bool {
	if runtime.GOOS != "windows" && strings.Contains(value, `\`) {
		return false
	}
	return filepath.IsAbs(value) && filepath.Clean(value) == value && len(value) <= 4096 && strings.IndexFunc(value, unicode.IsControl) < 0 && !strings.ContainsAny(value, "*?[]{}()!,")
}

func literalCommissionSkillName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i, c := range value {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && c == '-' {
			continue
		}
		return false
	}
	return true
}

// WorkBuddy normalizes Unix absolute rules with // but Windows drive rules
// must retain their C:/ prefix without an added leading slash.
func workBuddySkillReadRule(directory, platform string) (string, error) {
	path := filepath.ToSlash(directory)
	if platform == "windows" {
		path = strings.ReplaceAll(path, `\`, "/")
		if len(path) < 3 || !((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) || path[1] != ':' || path[2] != '/' {
			return "", errors.New("unsupported_workbuddy_skill_volume")
		}
		return "Read(" + path + "/**)", nil
	}
	return "Read(/" + path + "/**)", nil
}
