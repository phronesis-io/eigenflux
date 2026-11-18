package watchservice

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type launchLogPaths struct {
	StandardOutPath   string
	StandardErrorPath string
}

func (p launchLogPaths) validate(home string) error {
	if err := validateLaunchPath(home, "StandardOutPath", p.StandardOutPath); err != nil {
		return err
	}
	return validateLaunchPath(home, "StandardErrorPath", p.StandardErrorPath)
}

func validateLaunchPath(home, field, path string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("watch LaunchAgent %s %q must be an absolute path", field, path)
	}
	protected := func(root, candidate string) bool {
		candidate = strings.ToLower(filepath.Clean(candidate))
		for _, folder := range []string{"Desktop", "Documents", "Downloads", "Library/Mobile Documents"} {
			dir := strings.ToLower(filepath.Join(root, folder))
			if candidate == dir || strings.HasPrefix(candidate, dir+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	rejected := func() error {
		return fmt.Errorf("%w: %s %q; use an account-owned path in a local application-data directory", ErrProtectedLaunchPath, field, path)
	}
	if protected(home, path) {
		return rejected()
	}
	resolved, err := resolveLaunchPath(path)
	if err != nil {
		return fmt.Errorf("validate watch LaunchAgent %s %q: %w", field, path, err)
	}
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fmt.Errorf("resolve macOS user's home directory: %w", err)
	}
	if protected(home, resolved) || protected(realHome, resolved) {
		return rejected()
	}
	return nil
}

// A watch can create missing directories and log files. Resolve the existing
// ancestors in that case, while retaining errors for dangling symlinks.
func resolveLaunchPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if !os.IsNotExist(err) {
		return resolved, err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		return "", err
	} else if !os.IsNotExist(statErr) {
		return "", statErr
	}
	resolved, err = resolveLaunchPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}
