//go:build windows

package cmd

import (
	"encoding/base64"
	"os"
	"os/exec"

	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"
)

func reexecWatch(path string, args, env []string) error {
	key, _ := base64.StdEncoding.DecodeString(skills.VerifyPublicKeyBase64)
	running, err := os.Executable()
	if err != nil {
		return err
	}
	root, err := selfupdate.AdoptionInstallationRoot(running, path)
	if err != nil {
		return err
	}
	if _, err := selfupdate.ResolveInstallation(root, key); err == nil {
		if selfupdate.ManagedInstallationChild() {
			return &selfupdate.InstallationRestart{}
		}
		if err := selfupdate.RunInstallationChild(root, key, args, env); err != nil {
			return &updatedCLIError{err}
		}
		return nil
	} else if _, statErr := os.Lstat(root + ".current.json"); !os.IsNotExist(statErr) {
		return err
	}
	child := exec.Command(path, args...)
	child.Env = env
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	return runHeartbeatReexec(child)
}
