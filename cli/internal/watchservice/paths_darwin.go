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
	if !isProtectedLaunchPath(home, path) {
		resolved, err := resolveLaunchPath(path)
		if err != nil {
			return fmt.Errorf("validate watch LaunchAgent %s %q: %w", field, path, err)
		}
		realHome, err := filepath.EvalSymlinks(home)
		if err != nil {
			return fmt.Errorf("resolve macOS user's home directory: %w", err)
		}
		if !isProtectedLaunchPath(realHome, resolved) && !isProtectedLaunchPath(home, resolved) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s %q; use an account-owned path in a local application-data directory", ErrProtectedLaunchPath, field, path)
}

func isProtectedLaunchPath(home, path string) bool {
	candidate := strings.ToLower(filepath.Clean(path))
	for _, folder := range []string{"Desktop", "Documents", "Downloads", "Library/Mobile Documents"} {
		dir := strings.ToLower(filepath.Join(home, folder))
		if candidate == dir || strings.HasPrefix(candidate, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Log files may be new. Resolve the nearest existing ancestor and append
// the missing components. Lstat keeps dangling symlinks visible.
func resolveLaunchPath(path string) (string, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return filepath.EvalSymlinks(path)
	}
	parent := filepath.Dir(path)
	if !os.IsNotExist(err) || parent == path {
		return "", err
	}
	resolved, err := resolveLaunchPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}
