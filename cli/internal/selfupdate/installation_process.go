package selfupdate

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
)

// RunInstallationChild establishes a watch-only launcher after its identity
// has been fixed by the original watch. It never changes command arguments.
func RunInstallationChild(root string, key ed25519.PublicKey, args, env []string) error {
	if !pinnedWatchEnvironment(env) {
		return fmt.Errorf("installation watch restart requires pinned account identity")
	}
	installation, err := ResolveInstallation(root, key)
	if err != nil {
		return err
	}
	return runInstallationChildren(installation, key, args, InstallationEnvironment(env, root), true)
}

func runInstallationChildren(installation *Installation, key ed25519.PublicKey, args, env []string, managed bool) error {
	if managed {
		env = append(env, installWatchLauncherEnv+"=1")
	}
	// One stable parent waits for one version child. An adoption ends the old
	// child before starting the next, preventing an unbounded parent chain.
	for {
		child := exec.Command(installation.Executable, args...)
		child.Env = env
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		// Console interrupts are delivered to both processes in the same group.
		// Keep the parent alive while the child handles graceful shutdown. Do
		// not use signal.Ignore: Windows children inherit that ignore setting.
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt)
		err := child.Run()
		signal.Stop(interrupts)
		if err == nil {
			return nil
		}
		var exited *exec.ExitError
		if !errors.As(err, &exited) {
			return err
		}
		if exited.ExitCode() != InstallRestartExitCode || !managed {
			return err
		}
		next, err := ResolveInstallation(installation.Root, key)
		if err != nil {
			return fmt.Errorf("resolve installation after drained handoff: %w", err)
		}
		if Compare(next.Version, installation.Version) <= 0 {
			return fmt.Errorf("drained installation restart has no newer active release")
		}
		installation = next
	}
}
