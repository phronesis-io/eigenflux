package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/uninstallscript"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/spf13/cobra"
)

var regexpUninstallAttempt = regexp.MustCompile(`^[a-f0-9]{32}$`)

// No credentials or free-text reasons are carried in a finalization receipt.
type uninstallObservation struct {
	AttemptID      string              `json:"attempt_id"`
	Executable     string              `json:"executable"`
	CleanupFiles   []string            `json:"cleanup_files,omitempty"`
	FileDigests    map[string]string   `json:"file_digests,omitempty"`
	Homes          []installationHome  `json:"homes"`
	Scopes         []maintenance.Scope `json:"scopes,omitempty"`
	Started        time.Time           `json:"started"`
	Source         string              `json:"source"`
	ReasonCategory string              `json:"reason_category"`
	Version        string              `json:"version"`
	Pending        bool                `json:"pending"`
	Partial        bool                `json:"partial"`
	Mutated        bool                `json:"mutated"`
	Result         string              `json:"result,omitempty"`
	LocalReason    string              `json:"-"`
}

func newUninstallObservation(bin, source, category, reason string) *uninstallObservation {
	return &uninstallObservation{AttemptID: maintenance.NewID(), Executable: bin, Homes: []installationHome{{Home: config.HomeDir()}}, Started: time.Now().UTC(), Source: source, ReasonCategory: category, Version: version, LocalReason: reason}
}

func (o *uninstallObservation) discoverScopes() {
	restore := config.UseHomeDir(config.HomeDir())
	defer restore()
	seen := map[maintenance.Scope]bool{}
	for _, entry := range o.Homes {
		// An invalid registered Home must not redirect telemetry or create data.
		if !filepath.IsAbs(entry.Home) || filepath.Base(entry.Home) != ".eigenflux" {
			continue
		}
		config.SetHomeDir(entry.Home)
		data, err := os.ReadFile(filepath.Join(entry.Home, "config.json"))
		if err != nil {
			continue
		}
		var cfg config.Config
		if json.Unmarshal(data, &cfg) != nil {
			continue
		}
		for _, server := range cfg.Servers {
			if filepath.Base(server.Name) != server.Name || server.Name == "." || server.Name == ".." || server.Name == "" {
				continue
			}
			regular, err := auth.HasV2Credentials(server.Name)
			if err != nil || !regular {
				continue
			}
			credentials, err := auth.LoadV2Credentials(server.Name)
			if err != nil {
				continue
			}
			scope := maintenance.Scope{Home: entry.Home, Server: server.Name, AgentID: credentials.AgentID}
			if !seen[scope] {
				seen[scope] = true
				o.Scopes = append(o.Scopes, scope)
			}
		}
	}
}

func (o *uninstallObservation) emit(result, code string) {
	restore := config.UseHomeDir(config.HomeDir())
	defer restore()
	for _, scope := range o.Scopes {
		config.SetHomeDir(scope.Home)
		current, err := auth.LoadV2Credentials(scope.Server)
		if err != nil || current.AgentID != scope.AgentID {
			continue
		}
		e := maintenance.NewEvent(o.AttemptID, "cli", "manual", "uninstall", result)
		e.Source, e.Platform, e.ReasonCategory = o.Source, runtime.GOOS, o.ReasonCategory
		e.FromVersion, e.ErrorCode = o.Version, code
		for _, home := range o.Homes {
			if home.Home == scope.Home {
				e.Host = runtimeProduct(home.Host)
				break
			}
		}
		e.DurationMS = time.Since(o.Started).Milliseconds()
		if result == "started" {
			e.DurationMS = 0
		}
		if err := maintenance.Record(scope, e); err != nil {
			fmt.Fprintln(os.Stderr, "Uninstall telemetry could not be saved; removal continues.")
		}
	}
}

// One five-second budget for all accounts; no refresh/login or background daemon.
func (o *uninstallObservation) flush() {
	restore := config.UseHomeDir(config.HomeDir())
	defer restore()
	deadline := time.Now().Add(5 * time.Second)
	for _, scope := range o.Scopes {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		config.SetHomeDir(scope.Home)
		current, err := auth.LoadV2Credentials(scope.Server)
		if err != nil || current.AgentID != scope.AgentID {
			continue
		}
		cfg, err := config.Load()
		if err != nil {
			continue
		}
		server, err := cfg.GetActive(scope.Server)
		if err != nil {
			continue
		}
		c := client.New(strings.TrimRight(server.Endpoint, "/")+"/api/v2", current.AccessToken, version, clientMetaForServer(server))
		c.HTTPClient = &http.Client{Timeout: remaining}
		if err := maintenance.FlushUninstall(scope, time.Now(), o.AttemptID, func(events []maintenance.Event) error {
			credentials, err := auth.LoadV2Credentials(scope.Server)
			if err != nil || credentials.AgentID != scope.AgentID || credentials.AccessToken != current.AccessToken {
				return errors.New("uninstall telemetry identity changed")
			}
			_, err = c.Post("/maintenance/events:batch", map[string]interface{}{"events": events})
			return err
		}); err != nil {
			fmt.Fprintln(os.Stderr, "Uninstall telemetry remains local; upload failed or authentication expired.")
		}
	}
}

func (o *uninstallObservation) finish(result, code string) {
	o.Pending = false
	o.Result = result
	o.emit(result, code)
	o.audit(result, code)
	if path, err := uninstallReceiptPath(o.AttemptID); err == nil {
		if err := saveMaintenance(path, o); err != nil {
			fmt.Fprintln(os.Stderr, "Uninstall receipt could not be saved; inspect the local audit.")
		}
	}
	o.flush()
}

func (o *uninstallObservation) audit(result, code string) {
	seen := map[string]bool{}
	for _, entry := range o.Homes {
		if !filepath.IsAbs(entry.Home) || filepath.Base(entry.Home) != ".eigenflux" || seen[entry.Home] {
			continue
		}
		seen[entry.Home] = true
		// Preserve the voluntarily supplied reason when Windows finalizes later.
		reason := o.LocalReason
		if reason == "" {
			var previous struct {
				Reason    string `json:"reason"`
				AttemptID string `json:"attempt_id"`
			}
			if data, err := os.ReadFile(filepath.Join(entry.Home, "uninstall.json")); err == nil {
				_ = json.Unmarshal(data, &previous)
				if previous.AttemptID == o.AttemptID {
					reason = previous.Reason
				}
			}
		}
		if err := saveMaintenance(filepath.Join(entry.Home, "uninstall.json"), map[string]interface{}{"attempt_id": o.AttemptID, "reason": reason, "at": o.Started, "executable": o.Executable, "result": result, "error_code": code}); err != nil {
			fmt.Fprintln(os.Stderr, "Uninstall audit could not be saved; removal continues.")
		}
	}
}

func uninstallErrorCode(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	case strings.Contains(err.Error(), "watch"):
		return "watch_active"
	case strings.Contains(err.Error(), "host triggers"):
		return "host_cleanup_required"
	case strings.Contains(err.Error(), "shared installation"):
		return "shared_installation"
	case strings.Contains(err.Error(), "active"):
		return "installation_busy"
	default:
		return "uninstall_failed"
	}
}

func uninstallReceiptPath(attempt string) (string, error) {
	if !regexpUninstallAttempt.MatchString(attempt) {
		return "", errors.New("invalid uninstall attempt")
	}
	return filepath.Join(config.HomeDir(), "uninstall-"+attempt+".json"), nil
}

func (o *uninstallObservation) removeOriginal() error {
	// Run from the temporary trusted CLI, after the original process has exited.
	self, err := installedExecutable()
	if err != nil {
		return err
	}
	if self == o.Executable {
		return errors.New("use the temporary uninstall reporter")
	}
	var releases []func()
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	for _, path := range []string{o.Executable + ".install.lock", o.Executable + ".update.lock"} {
		info, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && !info.Mode().IsRegular() {
			return errors.New("installation lock must be a regular file")
		}
		release, err := watchstate.AcquirePath(path)
		if err != nil {
			return err
		}
		releases = append(releases, release)
	}
	seen := map[string]bool{}
	for _, entry := range o.Homes {
		if !filepath.IsAbs(entry.Home) || filepath.Base(entry.Home) != ".eigenflux" {
			return errors.New("invalid receipt Home")
		}
		if seen[entry.Home] {
			continue
		}
		seen[entry.Home] = true
		data, err := os.ReadFile(filepath.Join(entry.Home, "config.json"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		var cfg config.Config
		if len(data) > 0 && json.Unmarshal(data, &cfg) != nil {
			return errors.New("invalid receipt Home configuration")
		}
		locks, err := acquireUninstallWatchLocks(entry.Home, cfg.Servers)
		if err != nil {
			return err
		}
		releases = append(releases, locks...)
	}
	paths := append([]string{o.Executable}, o.CleanupFiles...)
	// Revalidate all original files before deleting any. Registration and update
	// writers use the same locks, so a new installation cannot be removed here.
	for _, path := range paths {
		if path != o.Executable && path != o.Executable+".previous" && path != o.Executable+".update.json" && path != o.Executable+".install.json" && path != o.Executable+".uninstall.sh" && path != o.Executable+".uninstall.ps1" {
			return errors.New("invalid cleanup path in receipt")
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("installation files must be regular")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if o.FileDigests[path] == "" || uninstallscript.Digest(data) != o.FileDigests[path] {
			return errors.New("installation changed before finalization")
		}
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		o.Mutated = true
	}
	return nil
}

func init() {
	finalize := &cobra.Command{Use: "uninstall-finalize", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		attempt, _ := cmd.Flags().GetString("attempt")
		path, err := uninstallReceiptPath(attempt)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("regular uninstall receipt required")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var o uninstallObservation
		if json.Unmarshal(data, &o) != nil || o.AttemptID != attempt || !filepath.IsAbs(o.Executable) || o.Started.IsZero() || time.Since(o.Started) < 0 || time.Since(o.Started) > 24*time.Hour {
			return errors.New("invalid uninstall receipt")
		}
		if !o.Pending {
			if o.Result == "removed" || o.Result == "partial" || o.Result == "blocked" || o.Result == "failed" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"attempt_id": attempt, "result": o.Result})
			}
			return errors.New("uninstall receipt is already finalized")
		}
		result, code := "removed", ""
		remove, _ := cmd.Flags().GetBool("remove-original")
		if remove {
			if err := o.removeOriginal(); err != nil {
				result, code = "partial", uninstallErrorCode(err)
			}
		}
		for _, target := range append([]string{o.Executable}, o.CleanupFiles...) {
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				result, code = "partial", "executable_or_sidecar_retained"
				break
			}
		}
		if o.Partial && result == "removed" {
			result = "partial"
			code = "managed_content_preserved"
		}
		o.Pending = false
		o.finish(result, code)
		if err := saveMaintenance(path, &o); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"attempt_id": attempt, "result": result})
	}}
	finalize.Flags().String("attempt", "", "pending uninstall attempt")
	finalize.Flags().Bool("remove-original", false, "remove unchanged receipt-owned original installation")
	rootCmd.AddCommand(finalize)
}
