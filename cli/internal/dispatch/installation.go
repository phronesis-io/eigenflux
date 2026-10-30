package dispatch

import (
	"os"
	"path/filepath"

	"cli.eigenflux.ai/internal/selfupdate"
)

// A persisted self-bridge follows its installation, while each admitted task
// keeps the running CLI. Third-party commands are never remapped.
func (r Runner) withCurrentWorkBuddyBridge() Runner {
	command := r.Binding.Command
	if r.Binding.Host != "workbuddy" || r.Binding.Mode != "acp" || len(command) < 3 || command[1] != "watch" || command[2] != "workbuddy-acp" {
		return r
	}
	current, err := os.Executable()
	if err != nil || !sameCLIInstallation(command[0], current) {
		return r
	}
	r.Binding.Command = append([]string(nil), command...)
	r.Binding.Command[0] = current
	return r
}

func sameCLIInstallation(selected, current string) bool {
	if !filepath.IsAbs(selected) {
		return false
	}
	selectedInfo, err := os.Stat(selected)
	if err != nil {
		return false
	}
	currentInfo, err := os.Stat(current)
	if err != nil {
		return false
	}
	if os.SameFile(selectedInfo, currentInfo) {
		return true
	}
	root, err := selfupdate.InstallationRoot(current)
	if err != nil || root == current {
		return false
	}
	selectedRoot, err := selfupdate.InstallationRoot(selected)
	return err == nil && selectedRoot == root
}
