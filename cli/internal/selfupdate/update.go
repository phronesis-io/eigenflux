// Package selfupdate installs signed stable CLI releases without touching Agent identity.
package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Options struct {
	// OnAttempt runs after the shared lock and throttle admit a real release check.
	OnAttempt                         func()
	Home                              string
	Executable, Version, Minimum, CDN string
	Key                               ed25519.PublicKey
	Client                            *http.Client
	Now                               time.Time
}

type Result struct {
	Phase      string `json:"phase,omitempty"`
	Attempted  bool   `json:"attempted,omitempty"`
	Status     string `json:"status"`
	Version    string `json:"version"`
	Error      string `json:"error,omitempty"`
	Executable string `json:"-"`
}

type state struct {
	Attempt time.Time `json:"attempt"`
	Minimum string    `json:"minimum,omitempty"`
	Highest string    `json:"highest,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Check retains the running executable on every pre-install failure. State and
// locking are binary-scoped because several Agent Homes can share one install.
func Check(ctx context.Context, o Options) (result Result) {
	result = Result{Status: "skipped", Version: o.Version, Phase: "check"}
	if !ValidVersion(o.Version) {
		return
	}
	if len(o.Key) != ed25519.PublicKeySize {
		result.Status = "unconfigured"
		return
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 30 * time.Second}
	}
	path, err := filepath.EvalSymlinks(o.Executable)
	if err != nil {
		result.Status, result.Error = "failed", err.Error()
		return
	}
	lock, err := os.OpenFile(path+".update.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		result.Status, result.Error = "failed", err.Error()
		return
	}
	defer lock.Close()
	locked, err := tryLock(lock)
	if err != nil {
		result.Status, result.Error = "failed", err.Error()
		return
	}
	if !locked {
		result.Status = "busy"
		return
	}
	defer unlock(lock)
	// A sibling heartbeat may have replaced the shared executable after this
	// process started. Adopt it instead of downgrading it or waiting for the TTL.
	if installed, e := executableVersion(ctx, path, o.Home); e == nil && ValidVersion(installed) && Compare(installed, o.Version) > 0 {
		return Result{Status: "updated", Version: installed, Executable: path, Phase: "adoption"}
	}
	var s state
	if b, e := os.ReadFile(path + ".update.json"); e == nil {
		_ = json.Unmarshal(b, &s)
	}
	if o.Now.Before(s.Attempt.Add(24*time.Hour)) && !o.Now.Before(s.Attempt) && (o.Minimum == "" || o.Minimum == s.Minimum) {
		result.Status, result.Error = "deferred", s.Error
		return
	}
	s.Attempt, s.Minimum = o.Now, o.Minimum
	result.Attempted = true
	if o.OnAttempt != nil {
		o.OnAttempt()
	}
	if err := saveState(path, s); err != nil {
		result.Status, result.Error = "failed", err.Error()
		return
	}
	defer func() {
		s.Error = result.Error
		if err := saveState(path, s); err != nil {
			result.Error = strings.TrimSpace(result.Error + "; save update state: " + err.Error())
		}
	}()
	phase := "check"
	fail := func(err error) Result {
		return Result{Status: "failed", Version: o.Version, Error: err.Error(), Phase: phase, Attempted: true}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	data, err := download(ctx, o.Client, strings.TrimRight(o.CDN, "/")+"/cli/latest/release.json", 1<<20, o.Version)
	if err != nil {
		return fail(err)
	}
	m, err := Verify(data, o.Key)
	if err != nil {
		return fail(err)
	}
	if ValidVersion(s.Highest) && Compare(m.Version, s.Highest) < 0 {
		return fail(fmt.Errorf("CLI release rollback rejected"))
	}
	s.Highest = m.Version
	if Compare(m.Version, o.Version) <= 0 {
		result.Status = "current"
		return
	}
	if o.Minimum != "" && (!ValidVersion(o.Minimum) || Compare(m.Version, o.Minimum) < 0) {
		return fail(fmt.Errorf("published CLI does not satisfy Skills minimum %s", o.Minimum))
	}
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	a, ok := m.Artifacts[name]
	if !ok || a.Size <= 0 || a.Size > 128<<20 {
		return fail(fmt.Errorf("missing or invalid CLI artifact for %s", name))
	}
	phase = "download"
	bin, err := download(ctx, o.Client, strings.TrimRight(o.CDN, "/")+"/cli/"+m.Version+"/"+name, a.Size, o.Version)
	if err != nil {
		return fail(err)
	}
	phase = "verify"
	sum := sha256.Sum256(bin)
	if int64(len(bin)) != a.Size || hex.EncodeToString(sum[:]) != a.SHA256 {
		return fail(fmt.Errorf("CLI artifact checksum or size mismatch"))
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".eigenflux-update-*.exe")
	if err != nil {
		return fail(err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(bin); err == nil {
		err = tmp.Chmod(0755)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fail(err)
	}
	phase = "probe"
	if err = probe(ctx, tmpName, m.Version, o.Home); err != nil {
		return fail(err)
	}
	phase = "install"
	// A hard link keeps the old bytes available without a gap at the installed path.
	backup := path + ".previous"
	if err = os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return fail(err)
	}
	if err = os.Link(path, backup); err != nil {
		return fail(fmt.Errorf("retain previous CLI: %w", err))
	}
	if err = syncDirectory(filepath.Dir(path)); err != nil {
		return fail(fmt.Errorf("persist CLI backup: %w", err))
	}
	if err = replace(tmpName, path); err != nil {
		return fail(fmt.Errorf("install CLI (existing binary retained): %w", err))
	}
	err = syncDirectory(filepath.Dir(path))
	if err == nil {
		phase = "probe"
		err = probe(ctx, path, m.Version, o.Home)
	}
	if err != nil {
		if restoreErr := replace(backup, path); restoreErr != nil {
			return fail(fmt.Errorf("CLI check failed: %v; restore %s failed: %w", err, backup, restoreErr))
		}
		if restoreErr := syncDirectory(filepath.Dir(path)); restoreErr != nil {
			return fail(fmt.Errorf("previous CLI restored but directory sync failed: %w", restoreErr))
		}
		return fail(fmt.Errorf("CLI check failed; previous binary restored: %w", err))
	}
	return Result{Status: "updated", Version: m.Version, Executable: path, Phase: "install", Attempted: true}
}

func probe(ctx context.Context, path, version, home string) error {
	got, err := executableVersion(ctx, path, home)
	if err != nil {
		return err
	}
	if got != version {
		return fmt.Errorf("new CLI reports unexpected version")
	}
	return nil
}

func executableVersion(ctx context.Context, path, home string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("CLI health check requires an absolute Agent Home")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--homedir", home, "version", "--short").Output()
	if err != nil {
		return "", fmt.Errorf("CLI health check: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func download(ctx context.Context, client *http.Client, url string, limit int64, version string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("User-Agent", "eigenflux/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CLI download HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("CLI download exceeds signed size limit")
	}
	return b, nil
}

func saveState(path string, s state) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".eigenflux-update-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = replace(f.Name(), path+".update.json"); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
