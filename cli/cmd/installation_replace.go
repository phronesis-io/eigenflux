package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

// Installers run the downloaded executable so Windows can replace the installed
// executable while holding the same locks as update and uninstall.
func replaceInstallation(source, destination string, entry installationHome) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || !filepath.IsAbs(entry.Home) {
		return fmt.Errorf("absolute installation paths required")
	}
	if source == destination {
		return fmt.Errorf("run installation replace from the downloaded executable")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return err
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	if existing, err := filepath.EvalSymlinks(destination); err == nil {
		destination = existing
	} else if !os.IsNotExist(err) {
		return err
	}
	if source == destination {
		return fmt.Errorf("run installation replace from the downloaded executable")
	}
	if entry.SkillsTarget != "" {
		entry.SkillsTarget, err = filepath.Abs(entry.SkillsTarget)
		if err != nil {
			return err
		}
		if target, err := filepath.EvalSymlinks(entry.SkillsTarget); err == nil {
			entry.SkillsTarget = target
		}
	}
	var releases []func()
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	for _, suffix := range []string{".install.lock", ".update.lock"} {
		release, err := watchstate.AcquirePath(destination + suffix)
		if err != nil {
			return err
		}
		releases = append(releases, release)
	}
	// A failed subsequent Skills sync must not erase an existing owner's target.
	if data, err := os.ReadFile(destination + ".install.json"); err == nil {
		var record installationRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.Version != 1 || record.Executable != destination {
			return fmt.Errorf("installation record mismatch")
		}
		if entry.SkillsTarget == "" {
			for _, previous := range record.Homes {
				if previous.Home == entry.Home && previous.Host == entry.Host {
					entry.SkillsTarget = previous.SkillsTarget
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	staged, err := os.CreateTemp(filepath.Dir(destination), ".eigenflux-install-*")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	_, copyErr := io.Copy(staged, in)
	if copyErr == nil {
		copyErr = staged.Chmod(0755)
	}
	if copyErr == nil {
		copyErr = staged.Sync()
	}
	closeErr := staged.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(staged.Name(), destination); err != nil {
		return err
	}
	return registerInstallationUnlocked(destination, entry)
}

func init() {
	command := &cobra.Command{Use: "replace", Short: "Install the downloaded CLI and record ownership under the installation lock", Args: cobra.NoArgs, Hidden: true}
	command.Flags().String("destination", "", "absolute installed executable path")
	command.Flags().String("host", "", "invoking Agent host")
	command.Flags().String("skills-target", "", "managed Skills directory")
	command.RunE = func(cmd *cobra.Command, _ []string) error {
		source, err := installedExecutable()
		if err != nil {
			return err
		}
		destination, _ := cmd.Flags().GetString("destination")
		home, err := watchstate.CanonicalHome(config.HomeDir())
		if err != nil {
			return err
		}
		host, _ := cmd.Flags().GetString("host")
		target, _ := cmd.Flags().GetString("skills-target")
		if err := replaceInstallation(source, destination, installationHome{home, host, target}); err != nil {
			return err
		}
		output.PrintData(map[string]string{"status": "installed", "executable": destination}, resolveFormat())
		return nil
	}
	installationCmd.AddCommand(command)
}
