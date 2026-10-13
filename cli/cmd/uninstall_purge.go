package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

func validateUninstallHome(home string) error {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || filepath.Base(home) != ".eigenflux" {
		return fmt.Errorf("registered Home must be an absolute .eigenflux directory")
	}
	info, err := os.Lstat(home)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("registered Home must be a real directory")
	}
	canonical, err := filepath.EvalSymlinks(home)
	if err != nil {
		return err
	}
	if canonical != home {
		return fmt.Errorf("registered Home must not traverse symlinks")
	}
	return nil
}

// Clear business data before terminal telemetry; retain live lock inodes.
func (o *uninstallObservation) purgeHomes() error {
	for _, entry := range o.Homes {
		if err := validateUninstallHome(entry.Home); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	lockName := regexp.MustCompile(`^[a-f0-9]{32}\.lock$`)
	for _, entry := range o.Homes {
		if seen[entry.Home] {
			continue
		}
		seen[entry.Home] = true
		protected := map[string]bool{o.Executable + ".install.lock": true, o.Executable + ".update.lock": true}
		for _, path := range o.RetainedPaths {
			protected[path] = true
		}
		if o.RemovalScope == "current" {
			for _, suffix := range []string{"", ".previous", ".update.json", ".install.json", ".uninstall.sh", ".uninstall.ps1"} {
				protected[o.Executable+suffix] = true
			}
		}
		watchDir := filepath.Join(entry.Home, "watch")
		if info, err := os.Lstat(watchDir); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			files, err := os.ReadDir(watchDir)
			if err != nil {
				return err
			}
			for _, file := range files {
				if lockName.MatchString(file.Name()) {
					protected[filepath.Join(watchDir, file.Name())] = true
				}
			}
		}
		var clear func(string) error
		clear = func(path string) error {
			if protected[path] {
				return nil
			}
			nested := false
			for held := range protected {
				if strings.HasPrefix(held, path+string(os.PathSeparator)) {
					nested = true
					break
				}
			}
			if !nested {
				return os.RemoveAll(path)
			}
			files, err := os.ReadDir(path)
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			for _, file := range files {
				if err := clear(filepath.Join(path, file.Name())); err != nil {
					return err
				}
			}
			files, err = os.ReadDir(path)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				return os.Remove(path)
			}
			return nil
		}
		if err := clear(entry.Home); err != nil {
			return fmt.Errorf("clear local Agent data: %w", err)
		}
	}
	return nil
}

// Hold credentials only in memory while clearing local files; report the actual
// cleanup outcome without recreating a queue, receipt, or credential file.
func (o *uninstallObservation) finishAndPurge(result, code string) error {
	restore := config.UseHomeDir(config.HomeDir())
	defer restore()
	type upload struct {
		c     *client.Client
		scope maintenance.Scope
	}
	uploads := []upload{}
	for _, scope := range o.Scopes {
		config.SetHomeDir(scope.Home)
		credentials, err := auth.LoadV2Credentials(scope.Server)
		if err != nil || credentials.AgentID != scope.AgentID {
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
		c := client.New(strings.TrimRight(server.Endpoint, "/")+"/api/v2", credentials.AccessToken, o.Version, clientMetaForServer(server))
		uploads = append(uploads, upload{c, scope})
	}
	var cleanupErr error
	if result == "removed" || result == "partial" {
		cleanupErr = o.purgeHomes()
		if cleanupErr == nil && o.RemovalScope == "current" {
			if o.RemainingRegistration == nil {
				cleanupErr = fmt.Errorf("remaining installation registration required")
			} else {
				cleanupErr = saveMaintenance(o.Executable+".install.json", o.RemainingRegistration)
			}
		}
		if cleanupErr != nil {
			result = "partial"
			code = "local_data_cleanup_failed"
		}
	}
	o.Pending = false
	o.Result = result
	if cleanupErr != nil {
		o.audit(result, code)
		if path, err := uninstallReceiptPath(o.AttemptID); err == nil {
			_ = saveMaintenance(path, o)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, u := range uploads {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		e := maintenance.NewEvent(o.AttemptID, "cli", "manual", "uninstall", result)
		e.Source, e.Platform, e.ReasonCategory = o.Source, runtime.GOOS, o.ReasonCategory
		e.FromVersion, e.ErrorCode = o.Version, code
		e.DurationMS = time.Since(o.Started).Milliseconds()
		for _, home := range o.Homes {
			if home.Home == u.scope.Home {
				e.Host = runtimeProduct(home.Host)
				break
			}
		}
		u.c.HTTPClient = &http.Client{Timeout: remaining}
		if _, err := u.c.Post("/maintenance/events:batch", map[string]interface{}{"events": []maintenance.Event{e}}); err != nil {
			fmt.Fprintln(os.Stderr, "Uninstall telemetry upload failed; local cleanup is not rolled back.")
		}
	}

	return cleanupErr
}
