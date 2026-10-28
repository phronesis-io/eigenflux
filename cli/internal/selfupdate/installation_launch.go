package selfupdate

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
)

// ForwardInstallation is called before command parsing. The stable entry is
// never overwritten, so Windows may keep it open while versions are upgraded.
// The caller must pin account-sensitive arguments/environment before launch.
// An invalid pointer is an error, not an implicit successful upgrade.
func ForwardInstallation(key ed25519.PublicKey, args, env []string, runningVersion ...string) (handled bool, exitCode int, err error) {
	executable, err := os.Executable()
	if err != nil {
		return false, 0, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return false, 0, err
	}
	root, err := InstallationRoot(executable)
	if err != nil {
		return false, 0, err
	}
	if root != executable {
		// Version children must not forward themselves back to the stable entry.
		return false, 0, nil
	}
	// An admitted task on the first bare watch still executes this stable
	// file. Internal CLI calls must finish on those bytes before watch drains.
	if os.Getenv(InstallChildEnv) == "1" && os.Getenv(InstallRootEnv) == root {
		return false, 0, nil
	}
	if _, err := os.Lstat(root + ".current.json"); os.IsNotExist(err) {
		return false, 0, nil
	}
	installation, err := ResolveInstallation(root, key)
	if err != nil {
		return false, 0, fmt.Errorf("resolve active installation: %w", err)
	}
	// A deliberate installer replacement of the stable entry may already be
	// newer than its last pointer. Never forward that installation backwards.
	if len(runningVersion) > 0 && ValidVersion(runningVersion[0]) && Compare(installation.Version, runningVersion[0]) < 0 {
		return false, 0, nil
	}
	return forwardInstallation(installation, key, args, InstallationEnvironment(env, root))
}
