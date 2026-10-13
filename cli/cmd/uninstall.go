package cmd

import (
	"context"
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
	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/skills"
	"cli.eigenflux.ai/internal/uninstallscript"
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
	Scripts    map[string]string  `json:"uninstall_scripts,omitempty"`
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
	return registerInstallationUnlocked(executable, entry)
}

// The caller holds the installation lock.
func registerInstallationUnlocked(executable string, entry installationHome) error {
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
	path, digest, err := uninstallscript.Install(executable, r.Scripts)
	if err != nil {
		return err
	}
	if r.Scripts == nil {
		r.Scripts = map[string]string{}
	}
	r.Scripts[path] = digest
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

var uninstallCmd = &cobra.Command{Use: "uninstall", Short: "Preview or remove a registered CLI installation; clear local Agent data", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) (retErr error) {
	bin, err := installedExecutable()
	if err != nil {
		return err
	}
	apply, _ := cmd.Flags().GetBool("apply")
	begin, _ := cmd.Flags().GetBool("begin")
	scope, _ := cmd.Flags().GetString("scope")
	allHomes, _ := cmd.Flags().GetBool("all-homes")
	if scope == "current" && allHomes {
		return errors.New("--all-homes cannot be combined with --scope current")
	}
	if scope == "" && allHomes {
		scope = "all"
	}
	if scope != "" && scope != "current" && scope != "all" {
		return errors.New("uninstall scope must be current or all")
	}
	if (apply || begin) && scope == "" {
		return errors.New("choose uninstall scope: do other Agents still need EigenFlux? use --scope current to keep the shared CLI, or --scope all to uninstall everything")
	}

	resumeAttempt, _ := cmd.Flags().GetString("attempt")
	recordResult, _ := cmd.Flags().GetString("record-result")
	if begin && apply {
		return errors.New("begin and apply are separate steps")
	}
	if recordResult != "" && (apply || begin || resumeAttempt == "" || recordResult != "blocked" && recordResult != "failed") {
		return errors.New("host cleanup result requires an existing attempt and blocked or failed")
	}
	protocol, _ := cmd.Flags().GetInt("script-protocol")
	if protocol != 0 && protocol != 1 {
		return errors.New("unsupported uninstall script protocol")
	}
	source, _ := cmd.Flags().GetString("source")
	category, _ := cmd.Flags().GetString("reason-category")
	reason, _ := cmd.Flags().GetString("reason")
	if category == "" {
		category = "not_provided"
		if strings.TrimSpace(reason) != "" {
			category = "other"
		}
	}
	probe := maintenance.NewEvent(maintenance.NewID(), "cli", "manual", "uninstall", "started")
	probe.Source, probe.Platform, probe.ReasonCategory = source, runtime.GOOS, category
	if err := maintenance.Validate(probe); err != nil {
		return err
	}
	var observation *uninstallObservation
	if apply || begin || recordResult != "" {
		observation = newUninstallObservation(bin, source, category, reason)
		observation.RemovalScope = scope
		if resumeAttempt != "" {
			path, err := uninstallReceiptPath(resumeAttempt)
			if err != nil {
				return err
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("regular uninstall attempt receipt required")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if json.Unmarshal(data, observation) != nil || observation.AttemptID != resumeAttempt || observation.Executable != bin || !observation.Pending || observation.Started.IsZero() || time.Since(observation.Started) < 0 || time.Since(observation.Started) > 24*time.Hour {
				return errors.New("uninstall attempt does not match this installation")
			}
			if scope != "" && observation.RemovalScope != scope {
				return errors.New("uninstall scope changed since begin; start a new attempt")
			}
			scope = observation.RemovalScope
			observation.LocalReason = reason
			if cmd.Flags().Changed("reason-category") {
				observation.ReasonCategory = category
			}
			if cmd.Flags().Changed("reason") && strings.TrimSpace(reason) != "" && observation.ReasonCategory == "not_provided" {
				observation.ReasonCategory = "other"
			}
			if reason == "" {
				var previous struct {
					AttemptID string `json:"attempt_id"`
					Reason    string `json:"reason"`
				}
				if data, err := os.ReadFile(filepath.Join(config.HomeDir(), "uninstall.json")); err == nil {
					_ = json.Unmarshal(data, &previous)
					if previous.AttemptID == resumeAttempt {
						observation.LocalReason = previous.Reason
						reason = previous.Reason
					}
				}
			}
			observation.Pending = false
		} else {
			observation.discoverScopes()
			observation.emit("started", "")
			observation.audit("started", "")
		}
		if recordResult != "" {
			observation.finish(recordResult, "host_cleanup_failed")
			path, _ := uninstallReceiptPath(observation.AttemptID)
			if err := saveMaintenance(path, observation); err != nil {
				return err
			}
			output.PrintData(map[string]string{"attempt_id": observation.AttemptID, "result": recordResult}, resolveFormat())
			return nil
		}
		defer func() {
			if retErr != nil && observation.Result == "" {
				result, code := "failed", uninstallErrorCode(retErr)
				if observation.Mutated {
					result = "partial"
				} else if code != "uninstall_failed" {
					result = "blocked"
				}
				observation.finish(result, code)
			} else if apply && !observation.Pending && observation.Result == "" {
				result := "removed"
				code := ""
				if observation.Partial {
					result = "partial"
					code = "managed_content_preserved"
				}
				observation.finish(result, code)
			}
		}()
	}
	var releases []func()
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	if apply || begin {
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
	if apply && resumeAttempt != "" {
		for _, path := range []string{bin, bin + ".install.json"} {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if observation.FileDigests[path] == "" || observation.FileDigests[path] != uninstallscript.Digest(data) {
				return errors.New("installation changed since uninstall began; inspect a fresh attempt")
			}
		}
	}
	plan, err := selectUninstallScope(r, config.HomeDir(), scope)
	if err != nil {
		return err
	}
	r.Homes = plan.Selected
	if observation != nil {
		observation.Homes = r.Homes
		observation.RemovalScope = scope
		observation.RetainedPaths = plan.RetainedPaths
		if scope == "current" {
			remaining := plan.Remaining
			observation.RemainingRegistration = &remaining
		}

		if resumeAttempt == "" {
			observation.Scopes = nil
			observation.discoverScopes()
			observation.emit("started", "")
			observation.audit("started", "")
		}
	}
	cleanup, _ := cmd.Flags().GetBool("host-cleanup-confirmed")
	if apply && !cleanup {
		return errors.New("stop and remove registered host triggers/plugins with their native tools before applying uninstall")
	}
	if len(reason) > 1000 {
		return errors.New("uninstall reason exceeds 1000 bytes")
	}
	removeSkills, _ := cmd.Flags().GetBool("skills")
	if !removeSkills {
		return errors.New("uninstall removes all registered EigenFlux Skills; --skills=false is unsupported")
	}
	removeNotifications, _ := cmd.Flags().GetBool("notifications")
	confirmNotifications, _ := cmd.Flags().GetBool("notification-cleanup-confirmed")
	if scope == "current" && removeNotifications {
		return errors.New("current-Agent uninstall must retain shared notification integration")
	}
	if apply && removeNotifications && !confirmNotifications {
		return errors.New("confirm all users of the shared notification integration before removal")
	}
	var skillResults []*skills.UninstallResult
	seenHome := map[string]bool{}
	seenTarget := map[string]bool{}
	for _, entry := range r.Homes {
		if err := validateUninstallHome(entry.Home); err != nil {
			return fmt.Errorf("invalid registered Home: %w", err)
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
		if removeSkills && entry.SkillsTarget != "" && !plan.SharedSkills[entry.SkillsTarget] && !seenTarget[entry.SkillsTarget] {
			seenTarget[entry.SkillsTarget] = true
			result, err := skills.Uninstall(entry.SkillsTarget, false)
			if err != nil {
				return err
			}
			skillResults = append(skillResults, result)
		}
	}
	var cleanupFiles, preservedScripts []string
	if scope != "current" {
		cleanupFiles, err = uninstallCleanupFiles(bin)
		if err != nil {
			return err
		}
		for path := range r.Scripts {
			if path != bin+".uninstall.sh" && path != bin+".uninstall.ps1" {
				return errors.New("invalid registered uninstall script")
			}
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				preservedScripts = append(preservedScripts, path)
				continue
			}
			cleanupFiles = append(cleanupFiles, path)
		}
	}
	if observation != nil && len(preservedScripts) > 0 {
		observation.Partial = true
	}
	if begin {
		observation.FileDigests = map[string]string{}
		for _, path := range append([]string{bin, bin + ".install.json"}, cleanupFiles...) {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			observation.FileDigests[path] = uninstallscript.Digest(data)
		}
		observation.Pending = true
		path, _ := uninstallReceiptPath(observation.AttemptID)
		if err := saveMaintenance(path, observation); err != nil {
			return err
		}
		observation.flush()
		output.PrintData(map[string]interface{}{"attempt_id": observation.AttemptID, "result": "started", "executable": bin, "homes": r.Homes, "uninstall_scripts": r.Scripts}, resolveFormat())
		return nil
	}
	if apply {
		if removeNotifications {
			// A native cleanup may partially mutate before returning an error.
			observation.Mutated = true
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			err := desktopnotify.Uninstall(ctx)
			cancel()
			if err != nil {
				return err
			}
		}
		for i, result := range skillResults {
			removed, err := skills.Uninstall(result.Target, true, true)
			if removed != nil && len(removed.Removed) > 0 {
				observation.Mutated = true
			}
			if err != nil {
				return err
			}
			if len(removed.Preserved) > 0 {
				observation.Partial = true
			}
			skillResults[i] = removed
		}
		if runtime.GOOS != "windows" && scope != "current" {
			if err := os.Remove(bin); err != nil {
				return err
			}
			observation.Mutated = true
			for _, path := range cleanupFiles {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
	}
	attempt, receiptHome := "", ""
	if observation != nil {
		attempt, receiptHome = observation.AttemptID, config.HomeDir()
		if runtime.GOOS == "windows" && scope != "current" {
			observation.audit("started", "")
			observation.Pending = true
			observation.CleanupFiles = cleanupFiles
			observation.FileDigests = map[string]string{}
			for _, path := range append([]string{bin, bin + ".install.json"}, cleanupFiles...) {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				observation.FileDigests[path] = uninstallscript.Digest(data)
			}
			path, err := uninstallReceiptPath(attempt)
			if err != nil {
				return err
			}
			if err := saveMaintenance(path, observation); err != nil {
				return err
			}
			observation.flush()
		}
	}
	notificationCleanup := "preserved_shared"
	if apply && removeNotifications {
		notificationCleanup = "removed"
	}
	result := "preview"
	if apply {
		result = "removed"
		if observation.Partial {
			result = "partial"
		}
		if observation.Pending {
			result = "pending"
		}
	}
	localDataRemoved := false
	if apply && (runtime.GOOS != "windows" || scope == "current") {
		code := ""
		if observation.Partial {
			code = "managed_content_preserved"
		}
		if err := observation.finishAndPurge(result, code); err != nil {
			return err
		}
		localDataRemoved = true
	}
	output.PrintData(map[string]interface{}{"result": result, "scope": scope, "scope_selection_required": scope == "", "scope_question": "Do other Agents still need EigenFlux?", "scope_choices": []string{"current", "all"}, "shared_cli_preserved": scope == "current", "shared_resources_preserved": plan.RetainedPaths, "notification_cleanup": notificationCleanup, "attempt_id": attempt, "receipt_home": receiptHome, "apply": apply, "executable": bin, "homes": r.Homes, "skills": skillResults, "cleanup_files": cleanupFiles, "uninstall_scripts": r.Scripts, "preserved_scripts": preservedScripts, "retained_lock_files": []string{bin + ".install.lock", bin + ".update.lock"}, "data_preserved": !localDataRemoved, "data_cleanup_pending": apply && runtime.GOOS == "windows" && scope != "current", "executable_removal_pending": apply && runtime.GOOS == "windows" && scope != "current", "reason_saved": apply && !localDataRemoved && strings.TrimSpace(reason) != "", "path_cleanup": "retain shared PATH entries; use host tools for scheduler/plugin cleanup"}, resolveFormat())
	if apply && result == "partial" {
		return fmt.Errorf("uninstall incomplete; retained managed paths: %v", preservedScripts)
	}
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
	uninstallCmd.Flags().String("scope", "", "current removes only this Agent; all removes the complete shared installation")
	uninstallCmd.Flags().Bool("begin", false, "record an uninstall attempt before native host cleanup; remove nothing")
	uninstallCmd.Flags().String("attempt", "", "continue an account-local uninstall attempt")
	uninstallCmd.Flags().String("record-result", "", "record blocked or failed native cleanup without deleting files")
	uninstallCmd.Flags().Int("script-protocol", 0, "official uninstall script protocol")
	_ = uninstallCmd.Flags().MarkHidden("script-protocol")
	uninstallCmd.Flags().String("source", "manual", "uninstall source: agent or manual")
	uninstallCmd.Flags().String("reason-category", "", "optional fixed uninstall reason category")
	uninstallCmd.Flags().Bool("notifications", false, "remove owned per-user notification integration")
	uninstallCmd.Flags().Bool("notification-cleanup-confirmed", false, "all users of the shared notification integration were reviewed")
	uninstallCmd.Flags().Bool("apply", false, "apply the inspected removal plan")
	uninstallCmd.Flags().Bool("all-homes", false, "remove a binary shared by all registered Homes")
	uninstallCmd.Flags().Bool("host-cleanup-confirmed", false, "native host triggers and plugin owners have been stopped and removed")
	uninstallCmd.Flags().Bool("skills", true, "remove registered EigenFlux Skills, including edited content (always enabled)")
	uninstallCmd.Flags().String("reason", "", "optional uninstall reason, temporary local audit erased after successful cleanup")
	remove := &cobra.Command{Use: "uninstall", Short: "Remove EigenFlux-managed Skills, including edited content, from one explicit target", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
	remove.Flags().Bool("apply", false, "remove owned managed skill directories, including edited content")
	skillsCmd.AddCommand(remove)
}
