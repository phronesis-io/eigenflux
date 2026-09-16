package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/skills"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

type installationHome struct {
	Home         string `json:"home"`
	Host         string `json:"host"`
	SkillsTarget string `json:"skills_target,omitempty"`
}
type installationRecord struct {
	Version    int                `json:"version"`
	Executable string             `json:"executable"`
	Homes      []installationHome `json:"homes"`
}

func installedExecutable() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

func registerInstallation(executable string, entry installationHome) error {
	if !filepath.IsAbs(executable) || !filepath.IsAbs(entry.Home) {
		return fmt.Errorf("absolute installation paths required")
	}
	release, err := watchstate.AcquirePath(executable + ".install.lock")
	if err != nil {
		return err
	}
	defer release()
	r := installationRecord{Version: 1, Executable: executable}
	if b, err := os.ReadFile(executable + ".install.json"); err == nil {
		if err = json.Unmarshal(b, &r); err != nil {
			return err
		}
		if r.Executable != executable || r.Version != 1 {
			return errors.New("installation record mismatch")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for i, e := range r.Homes {
		if e.Home == entry.Home && e.Host == entry.Host {
			r.Homes[i] = entry
			return saveMaintenance(executable+".install.json", r)
		}
	}
	r.Homes = append(r.Homes, entry)
	return saveMaintenance(executable+".install.json", r)
}

var installationCmd = &cobra.Command{Use: "installation", Short: "Inspect or register this explicit CLI installation"}
var installationRecordCmd = &cobra.Command{Use: "record", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	bin, err := installedExecutable()
	if err != nil {
		return err
	}
	home, err := watchstate.CanonicalHome(config.HomeDir())
	if err != nil {
		return err
	}
	host, _ := cmd.Flags().GetString("host")
	target, _ := cmd.Flags().GetString("skills-target")
	if !cmd.Flags().Changed("skills-target") {
		target, err = skills.ResolveSkillsDir("", host)
		if err != nil {
			return err
		}
	}
	if target != "" {
		target, err = filepath.Abs(target)
		if err != nil {
			return err
		}
		if canonical, e := filepath.EvalSymlinks(target); e == nil {
			target = canonical
		}
	}
	if err = registerInstallation(bin, installationHome{home, host, target}); err != nil {
		return err
	}
	output.PrintData(map[string]string{"status": "registered", "executable": bin, "home": home}, resolveFormat())
	return nil
}}

var uninstallCmd = &cobra.Command{Use: "uninstall", Short: "Preview or remove a registered CLI installation; preserve Agent data", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	bin, err := installedExecutable()
	if err != nil {
		return err
	}
	apply, _ := cmd.Flags().GetBool("apply")
	var releases []func()
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	if apply {
		for _, lockPath := range []string{bin + ".install.lock", bin + ".update.lock"} {
			if info, e := os.Lstat(lockPath); e == nil && !info.Mode().IsRegular() {
				return errors.New("installation lock must be a regular file")
			} else if e != nil && !os.IsNotExist(e) {
				return e
			}
			release, e := watchstate.AcquirePath(lockPath)
			if e != nil {
				return fmt.Errorf("installation registration or update is active: %w", e)
			}
			releases = append(releases, release)
		}
	}
	b, err := os.ReadFile(bin + ".install.json")
	if err != nil {
		return fmt.Errorf("installation record required: %w", err)
	}
	var r installationRecord
	if json.Unmarshal(b, &r) != nil || r.Version != 1 || r.Executable != bin || len(r.Homes) == 0 {
		return errors.New("invalid installation record")
	}
	all, _ := cmd.Flags().GetBool("all-homes")
	cleanup, _ := cmd.Flags().GetBool("host-cleanup-confirmed")
	if apply && !cleanup {
		return errors.New("stop and remove registered host triggers/plugins with their native tools before applying uninstall")
	}
	if apply && len(r.Homes) > 1 && !all {
		return errors.New("shared installation: --all-homes is required after reviewing every registered Home")
	}
	reason, _ := cmd.Flags().GetString("reason")
	if len(reason) > 1000 {
		return errors.New("uninstall reason exceeds 1000 bytes")
	}
	removeSkills, _ := cmd.Flags().GetBool("skills")
	var skillResults []*skills.UninstallResult
	seenHome := map[string]bool{}
	seenTarget := map[string]bool{}
	for _, entry := range r.Homes {
		if !filepath.IsAbs(entry.Home) {
			return errors.New("invalid registered Home")
		}
		if !seenHome[entry.Home] {
			seenHome[entry.Home] = true
			// Read the exact registered configuration without switching global Home.
			data, err := os.ReadFile(filepath.Join(entry.Home, "config.json"))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			var cfg config.Config
			if len(data) > 0 && json.Unmarshal(data, &cfg) != nil {
				return errors.New("invalid registered Home configuration")
			}
			if apply {
				watchReleases, err := acquireUninstallWatchLocks(entry.Home, cfg.Servers)
				if err != nil {
					return err
				}
				releases = append(releases, watchReleases...)
			}
		}
		if removeSkills && entry.SkillsTarget != "" && !seenTarget[entry.SkillsTarget] {
			seenTarget[entry.SkillsTarget] = true
			result, err := skills.Uninstall(entry.SkillsTarget, false)
			if err != nil {
				return err
			}
			skillResults = append(skillResults, result)
		}
	}
	cleanupFiles, err := uninstallCleanupFiles(bin)
	if err != nil {
		return err
	}
	if apply {
		for _, entry := range r.Homes {
			if err := saveMaintenance(filepath.Join(entry.Home, "uninstall.json"), map[string]interface{}{"reason": reason, "at": time.Now().UTC(), "executable": bin}); err != nil {
				return err
			}
		}
		for i, result := range skillResults {
			removed, err := skills.Uninstall(result.Target, true)
			if err != nil {
				return err
			}
			skillResults[i] = removed
		}
		if runtime.GOOS != "windows" {
			if err := os.Remove(bin); err != nil {
				return err
			}
			for _, path := range cleanupFiles {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
	}
	output.PrintData(map[string]interface{}{"apply": apply, "executable": bin, "homes": r.Homes, "skills": skillResults, "cleanup_files": cleanupFiles, "retained_lock_files": []string{bin + ".install.lock", bin + ".update.lock"}, "data_preserved": true, "executable_removal_pending": apply && runtime.GOOS == "windows", "reason_saved": apply && strings.TrimSpace(reason) != "", "path_cleanup": "retain shared PATH entries; use host tools for scheduler/plugin cleanup"}, resolveFormat())
	return nil
}}

// Include retained lock files even when a server has since been removed from config.
func acquireUninstallWatchLocks(home string, servers []config.Server) ([]func(), error) {
	paths := map[string]bool{}
	for _, server := range servers {
		paths[filepath.Join(home, "watch", watchstate.Scope(home, server.Name, "", "")+".lock")] = true
	}
	entries, err := os.ReadDir(filepath.Join(home, "watch"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	valid := regexp.MustCompile(`^[a-f0-9]{32}\.lock$`)
	for _, entry := range entries {
		if valid.MatchString(entry.Name()) {
			paths[filepath.Join(home, "watch", entry.Name())] = true
		}
	}
	var releases []func()
	fail := func(err error) ([]func(), error) {
		for _, release := range releases {
			release()
		}
		return nil, err
	}
	for path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return fail(err)
			}
		} else if err != nil {
			return fail(err)
		} else if !info.Mode().IsRegular() {
			return fail(errors.New("watch lock must be a regular file"))
		}
		release, err := watchstate.AcquirePath(path)
		if err != nil {
			return fail(fmt.Errorf("stop active watch in %s: %w", home, err))
		}
		releases = append(releases, release)
	}
	return releases, nil
}

func uninstallCleanupFiles(bin string) ([]string, error) {
	var paths []string
	for _, suffix := range []string{".previous", ".update.json", ".install.json"} {
		path := bin + suffix
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("installation sidecar must be a regular file: %s", path)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func init() {
	installationRecordCmd.Flags().String("host", "", "invoking host owning this installation registration")
	installationRecordCmd.Flags().String("skills-target", "", "actual Skills target used by this installer; explicit empty records no target")
	installationCmd.AddCommand(installationRecordCmd)
	rootCmd.AddCommand(installationCmd, uninstallCmd)
	uninstallCmd.Flags().Bool("apply", false, "apply the inspected removal plan")
	uninstallCmd.Flags().Bool("all-homes", false, "remove a binary shared by all registered Homes")
	uninstallCmd.Flags().Bool("host-cleanup-confirmed", false, "native host triggers and plugin owners have been stopped and removed")
	uninstallCmd.Flags().Bool("skills", false, "remove unchanged managed Skills at registered targets")
	uninstallCmd.Flags().String("reason", "", "optional uninstall reason, retained locally in Agent Home")
	remove := &cobra.Command{Use: "uninstall", Short: "Remove unchanged EigenFlux-managed Skills from one explicit target", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		target, _ := cmd.Flags().GetString("into")
		if target == "" {
			return errors.New("explicit --into target required; shared host Skills affect all accounts")
		}
		apply, _ := cmd.Flags().GetBool("apply")
		r, err := skills.Uninstall(target, apply)
		if err != nil {
			return err
		}
		output.PrintData(r, resolveFormat())
		return nil
	}}
	remove.Flags().String("into", "", "exact managed Skills target")
	remove.Flags().Bool("apply", false, "remove unchanged managed skill directories")
	skillsCmd.AddCommand(remove)
}
