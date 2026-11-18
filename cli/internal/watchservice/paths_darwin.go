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
	for _, log := range []struct{ field, path string }{
		{"StandardOutPath", p.StandardOutPath},
		{"StandardErrorPath", p.StandardErrorPath},
	} {
		if !filepath.IsAbs(log.path) {
			continue
		}
		if isProtectedLaunchPath(home, log.path) || isProtectedLogTarget(home, log.path) {
			return fmt.Errorf("watch LaunchAgent %s %q is in a macOS privacy-protected folder; use a local application-data path", log.field, log.path)
		}
	}
	return nil
}

func isProtectedLogTarget(home, path string) bool {
	resolved, err := resolveLaunchPath(path)
	if err != nil {
		return false
	}
	realHome, err := filepath.EvalSymlinks(home)
	return isProtectedLaunchPath(home, resolved) || (err == nil && isProtectedLaunchPath(realHome, resolved))
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
