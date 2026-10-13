// Package pluginupdate updates an installed EigenFlux plugin through its host manager.
package pluginupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/selfupdate"
	watchstate "cli.eigenflux.ai/internal/watch"
)

const PluginID = "openclaw-eigenflux"
const PackageName = "@phronesis-io/openclaw-eigenflux"
const Registry = "https://registry.npmjs.org"

type Run func(context.Context, ...string) ([]byte, error)
type Options struct {
	Run                   Run
	Client                *http.Client
	Registry              string
	PollInterval          time.Duration
	Server                string
	CLIVersion            string
	RestartPendingVersion string
}
type Result struct {
	Status                string `json:"status"`
	Scope                 string `json:"scope,omitempty"`
	InstalledVersion      string `json:"installed_version,omitempty"`
	LatestVersion         string `json:"latest_version,omitempty"`
	LoadedVersion         string `json:"loaded_version,omitempty"`
	RestartPendingVersion string `json:"restart_pending_version,omitempty"`
	Error                 string `json:"error,omitempty"`
}
type inspection struct {
	Plugin struct {
		ID, PackageName, Version, RootDir, Status string
		Enabled                                   bool
		Trust                                     struct{ Reason, RegistryPath string }
	}
	Install struct {
		Source, Spec, SourcePath, ArtifactKind, InstallPath, Version string
		ResolvedName, ResolvedVersion, ResolvedSpec, Integrity       string
	}
}
type release struct {
	Name, Version string
	OpenClaw      struct {
		Compat struct{ MinGatewayVersion string }
	}
	Dist struct{ Tarball, Integrity string }
}
type runtimeStatus struct {
	Schema        string `json:"schema_version"`
	PluginVersion string `json:"plugin_version"`
	GatewayPID    int    `json:"gateway_pid"`
	Busy          bool   `json:"busy"`
	Servers       []struct {
		Server     string `json:"server"`
		CLIVersion string `json:"cli_version"`
		Ready      bool   `json:"ready"`
		Running    bool   `json:"running"`
	} `json:"servers"`
}
type gatewayStatus struct {
	CLI     struct{ Version string }
	Gateway struct{ Version, ProbeURL string }
	Service struct {
		Runtime struct {
			PID    int
			Status string
		}
	}
	RPC struct {
		OK  bool
		URL string
	}
	Config struct {
		CLI    struct{ Path string }
		Daemon struct{ Path string }
	}
}

// Update preserves disabled/absent plugins, source selectors, and host configuration.
// The caller runs it outside the Gateway so a source-change restart cannot kill it.
func Update(ctx context.Context, o Options) (r Result) {
	r.Status = "failed"
	if selfupdate.ValidVersion(o.RestartPendingVersion) {
		r.RestartPendingVersion = o.RestartPendingVersion
	}
	fail := func(status string, err error) Result { r.Status, r.Error = status, err.Error(); return r }
	if o.Run == nil {
		return fail("failed", errors.New("OpenClaw command runner required"))
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Registry == "" {
		o.Registry = Registry
	}
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	read := func(args ...string) (inspection, error) {
		var i inspection
		b, err := o.Run(ctx, args...)
		if err == nil {
			err = json.Unmarshal(b, &i)
		}
		return i, err
	}
	listed, err := o.Run(ctx, "plugins", "list", "--json")
	var inventory struct{ Plugins []struct{ ID string } }
	if err == nil {
		err = json.Unmarshal(listed, &inventory)
	}
	if err != nil {
		return fail("failed", fmt.Errorf("list installed plugins: %w", err))
	}
	present := false
	for _, p := range inventory.Plugins {
		if p.ID == PluginID {
			present = true
			break
		}
	}
	if !present {
		r.Status = "not_installed"
		return r
	}
	i, err := read("plugins", "inspect", PluginID, "--json")
	if err != nil {
		return fail("failed", fmt.Errorf("inspect installed plugin: %w", err))
	}
	if i.Plugin.ID == "" {
		r.Status = "not_installed"
		return r
	}
	r.InstalledVersion = i.Plugin.Version
	r.Scope = i.Install.InstallPath
	if !i.Plugin.Enabled {
		return fail("blocked", errors.New("plugin is disabled"))
	}
	if err := validateInstall(i); err != nil {
		return fail("blocked", err)
	}
	// The registry path is host-scoped, so several Agent Homes cannot update one Gateway concurrently.
	lockDir := filepath.Dir(i.Plugin.Trust.RegistryPath)
	if !filepath.IsAbs(lockDir) || i.Plugin.Trust.RegistryPath == "" {
		return fail("blocked", errors.New("absolute host plugin registry path required"))
	}
	releaseLock, err := watchstate.AcquirePath(filepath.Join(lockDir, "eigenflux-plugin-update.lock"))
	if err != nil {
		return fail("blocked", errors.New("another plugin update owns this host"))
	}
	defer releaseLock()
	// Re-read under the host lock; installation may have changed before acquisition.
	i, err = read("plugins", "inspect", PluginID, "--json")
	if err != nil {
		return fail("failed", err)
	}
	if err := validateInstall(i); err != nil {
		return fail("blocked", err)
	}
	if !i.Plugin.Enabled {
		return fail("blocked", errors.New("plugin is disabled"))
	}
	r.InstalledVersion, r.Scope = i.Plugin.Version, i.Install.InstallPath
	gateway, port, err := readLocalGateway(ctx, o)
	if err != nil {
		return fail("blocked", err)
	}
	selected, err := discoverVersion(ctx, o, i)
	if err != nil {
		return fail("blocked", err)
	}
	meta, err := fetchRelease(ctx, o, selected)
	if err != nil {
		return fail("failed", err)
	}
	r.LatestVersion = meta.Version
	if meta.Name != PackageName || meta.Version != selected || !selfupdate.ValidVersion(meta.Version) {
		return fail("blocked", errors.New("published plugin identity differs from the host-selected release"))
	}
	if selfupdate.Compare(meta.Version, r.InstalledVersion) < 0 {
		return fail("blocked", errors.New("plugin downgrade rejected"))
	}
	// OpenClaw's official-catalog label is not a package integrity verdict.
	installed, err := fetchRelease(ctx, o, r.InstalledVersion)
	if err != nil {
		return fail("failed", err)
	}
	if installed.Name != PackageName || installed.Version != r.InstalledVersion || i.Install.Integrity != installed.Dist.Integrity {
		return fail("blocked", errors.New("installed npm identity or integrity differs from the publisher"))
	}
	if err := verifyFiles(ctx, o, installed, i.Plugin.RootDir); err != nil {
		return fail("blocked", err)
	}
	if r.RestartPendingVersion != r.InstalledVersion || r.RestartPendingVersion != r.LatestVersion {
		r.RestartPendingVersion = ""
	}
	before, runtimeErr := readRuntime(ctx, o, port)
	if runtimeErr == nil && before.GatewayPID != gateway.Service.Runtime.PID {
		return fail("blocked", errors.New("plugin runtime belongs to another Gateway process"))
	}
	if runtimeErr == nil && before.Busy {
		return fail("blocked", errors.New("EigenFlux deliveries are active; retry when idle"))
	}
	changed := r.InstalledVersion != r.LatestVersion
	if !changed && runtimeErr == nil && before.PluginVersion == r.InstalledVersion && runtimeReady(before, o) {
		r.Status, r.LoadedVersion = "loaded", before.PluginVersion
		r.RestartPendingVersion = ""
		return r
	}
	if !changed && runtimeErr != nil && r.RestartPendingVersion == "" {
		// Older adapters cannot attest their loaded version. Do not restart them every cycle.
		return fail("restart_required", errors.New("installed plugin lacks live runtime evidence; use an adapter with eigenflux.runtime support"))
	}
	if !changed && runtimeErr == nil && before.PluginVersion == r.InstalledVersion {
		return fail("restart_required", errors.New("plugin is loaded but its account watch is not ready"))
	}
	if changed {
		// The host manager owns package verification, registry writes, and source-change restart.
		if _, err := o.Run(ctx, "plugins", "update", PluginID); err != nil {
			return fail("failed", fmt.Errorf("host plugin update failed: %w", err))
		}
		i, err = read("plugins", "inspect", PluginID, "--json")
		if err != nil {
			return fail("failed", err)
		}
		if err := validateInstall(i); err != nil {
			return fail("blocked", err)
		}
		r.InstalledVersion, r.Scope = i.Plugin.Version, i.Install.InstallPath
		if r.InstalledVersion != r.LatestVersion {
			return fail("failed", errors.New("host manager did not install the discovered version"))
		}
		if i.Install.Integrity != meta.Dist.Integrity {
			return fail("blocked", errors.New("updated install integrity differs from npm"))
		}
		if err := verifyFiles(ctx, o, meta, i.Plugin.RootDir); err != nil {
			return fail("blocked", err)
		}
		r.RestartPendingVersion = r.LatestVersion
	}
	// The manager owns restart deferral. Repair a lost metadata notification once,
	// without stopping the Gateway or restarting a watch that is only not ready.
	refreshed := false
	var refreshErr error
	for ctx.Err() == nil {
		local, localPort, statusErr := readLocalGateway(ctx, o)
		if statusErr == nil && localPort != port {
			return fail("blocked", errors.New("local Gateway endpoint changed during maintenance"))
		}
		current, err := readRuntime(ctx, o, port)
		bound := statusErr == nil && err == nil && current.GatewayPID == local.Service.Runtime.PID
		if bound && current.PluginVersion == r.LatestVersion && (!changed || current.GatewayPID != gateway.Service.Runtime.PID) && runtimeReady(current, o) {
			r.Status, r.LoadedVersion = "loaded", current.PluginVersion
			r.RestartPendingVersion = ""
			return r
		}
		oldRuntime := bound && current.PluginVersion != r.LatestVersion && !current.Busy
		// Only a verified completed install can bridge an adapter without this RPC.
		missingRuntime := statusErr == nil && err != nil && r.RestartPendingVersion == r.LatestVersion && r.RestartPendingVersion != ""
		if !refreshed && statusErr == nil && local.Service.Runtime.PID == gateway.Service.Runtime.PID && (oldRuntime || missingRuntime) {
			refreshed = true
			// Repeated notification remains a managed, idle-deferred restart request.
			_, refreshErr = o.Run(ctx, "gateway", "call", "plugins.refresh", "--json", "--port", port)
		}
		timer := time.NewTimer(o.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if refreshErr != nil {
		return fail("restart_required", fmt.Errorf("live plugin adoption is not yet verified; metadata notification failed: %w", refreshErr))
	}
	return fail("restart_required", errors.New("host restart is pending; live plugin adoption is not yet verified"))
}

func runtimeReady(r runtimeStatus, o Options) bool {
	if len(r.Servers) == 0 {
		return false
	}
	matched := o.Server == ""
	for _, s := range r.Servers {
		if !s.Ready || !s.Running || s.CLIVersion == "" || o.CLIVersion != "" && s.CLIVersion != o.CLIVersion {
			return false
		}
		if s.Server == o.Server {
			matched = true
		}
	}
	return matched
}

func validateInstall(i inspection) error {
	s := i.Install
	if i.Plugin.ID != PluginID || i.Plugin.PackageName != PackageName || s.Source != "npm" || s.SourcePath != "" || s.ArtifactKind != "" || !selfupdate.ValidVersion(i.Plugin.Version) || s.Version != i.Plugin.Version || s.ResolvedName != PackageName || s.ResolvedVersion != i.Plugin.Version || s.ResolvedSpec != PackageName+"@"+i.Plugin.Version {
		return errors.New("EigenFlux npm install record is inconsistent")
	}
	if s.Spec != PackageName && s.Spec != PackageName+"@latest" && s.Spec != PackageName+"@"+i.Plugin.Version {
		return errors.New("unsupported plugin source selector")
	}
	a, ea := filepath.EvalSymlinks(i.Plugin.RootDir)
	b, eb := filepath.EvalSymlinks(s.InstallPath)
	if ea != nil || eb != nil || !filepath.IsAbs(a) || a != b {
		return errors.New("plugin root does not match its recorded install path")
	}
	switch i.Plugin.Trust.Reason {
	case "trusted-official", "provenance-invalid":
		return nil
	default:
		return fmt.Errorf("unsupported plugin trust state: %s", i.Plugin.Trust.Reason)
	}
}

func readRuntime(ctx context.Context, o Options, port string) (runtimeStatus, error) {
	var r runtimeStatus
	b, err := o.Run(ctx, "gateway", "call", "eigenflux.runtime", "--json", "--port", port)
	if err == nil {
		err = json.Unmarshal(b, &r)
	}
	if err == nil && (r.Schema != "eigenflux_runtime.v1" || r.GatewayPID <= 1 || !selfupdate.ValidVersion(r.PluginVersion)) {
		err = errors.New("invalid plugin runtime evidence")
	}
	return r, err
}

func readLocalGateway(ctx context.Context, o Options) (gatewayStatus, string, error) {
	var g gatewayStatus
	b, err := o.Run(ctx, "gateway", "status", "--json")
	if err == nil {
		err = json.Unmarshal(b, &g)
	}
	if err != nil || !g.RPC.OK || g.Service.Runtime.Status != "running" || g.Service.Runtime.PID <= 1 || g.Config.CLI.Path == "" || g.Config.CLI.Path != g.Config.Daemon.Path {
		return g, "", errors.New("a running local managed Gateway with matching CLI and daemon configuration is required")
	}
	u, err := url.Parse(g.Gateway.ProbeURL)
	if err != nil || g.Gateway.ProbeURL != g.RPC.URL || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "ws" && u.Scheme != "wss") {
		return g, "", errors.New("matching local Gateway probe and RPC URLs required")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	port, portErr := strconv.Atoi(u.Port())
	if (host != "localhost" && (ip == nil || !ip.IsLoopback())) || portErr != nil || port < 1 || port > 65535 {
		return g, "", errors.New("a loopback Gateway endpoint with an explicit port is required")
	}
	return g, strconv.Itoa(port), nil
}

var hostUpdateOutcome = regexp.MustCompile(`(?m)^Would (?:update|downgrade) openclaw-eigenflux: ([0-9]+\.[0-9]+\.[0-9]+) -> ([0-9]+\.[0-9]+\.[0-9]+)\.(?:[^\r\n]*)$`)
var hostUnchangedOutcome = regexp.MustCompile(`(?m)^openclaw-eigenflux is up to date \(([0-9]+\.[0-9]+\.[0-9]+)\)\.(?:[^\r\n]*)$`)
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func discoverVersion(ctx context.Context, o Options, i inspection) (string, error) {
	// Exact version selectors remain pinned even when another registry line exists.
	if i.Install.Spec == PackageName+"@"+i.Plugin.Version {
		return i.Plugin.Version, nil
	}
	b, err := o.Run(ctx, "plugins", "update", PluginID, "--dry-run")
	if err != nil {
		return "", fmt.Errorf("host compatibility discovery failed: %w", err)
	}
	output := strings.ReplaceAll(ansiEscape.ReplaceAllString(string(b), ""), "\r\n", "\n")
	updated := hostUpdateOutcome.FindAllStringSubmatch(output, -1)
	unchanged := hostUnchangedOutcome.FindAllStringSubmatch(output, -1)
	if len(updated) == 1 && len(unchanged) == 0 && updated[0][1] == i.Plugin.Version {
		return updated[0][2], nil
	}
	if len(unchanged) == 1 && len(updated) == 0 && unchanged[0][1] == i.Plugin.Version {
		return unchanged[0][1], nil
	}
	return "", errors.New("host dry-run did not provide a supported compatible-version outcome")
}

func fetchRelease(ctx context.Context, o Options, version string) (release, error) {
	var r release
	b, err := get(ctx, o, strings.TrimRight(o.Registry, "/")+"/@phronesis-io%2fopenclaw-eigenflux/"+url.PathEscape(version), 2<<20)
	if err == nil {
		err = json.Unmarshal(b, &r)
	}
	return r, err
}

func get(ctx context.Context, o Options, target string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("npm source returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = errors.New("npm response exceeds limit")
	}
	return b, err
}

func verifyFiles(ctx context.Context, o Options, r release, root string) error {
	target, err := url.Parse(r.Dist.Tarball)
	registry, registryErr := url.Parse(o.Registry)
	if err != nil || registryErr != nil || target.Scheme != registry.Scheme || target.Host != registry.Host || target.User != nil {
		return errors.New("published tarball does not belong to the configured npm registry")
	}
	b, err := get(ctx, o, r.Dist.Tarball, 16<<20)
	if err != nil {
		return err
	}
	sum := sha512.Sum512(b)
	if r.Dist.Integrity != "sha512-"+base64.StdEncoding.EncodeToString(sum[:]) {
		return errors.New("npm tarball integrity failed")
	}
	z, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer z.Close()
	f, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer f.Close()
	t := tar.NewReader(io.LimitReader(z, 32<<20))
	seen := map[string]bool{}
	for {
		h, err := t.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		name := strings.TrimPrefix(h.Name, "package/")
		if name == h.Name || !filepath.IsLocal(name) || seen[name] || h.Typeflag != tar.TypeReg || h.Size > 16<<20 {
			return errors.New("invalid npm package archive member")
		}
		seen[name] = true
		info, err := f.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("installed package file is unavailable: %s", name)
		}
		local, err := f.ReadFile(name)
		if err != nil {
			return err
		}
		published, err := io.ReadAll(t)
		if err != nil || !bytes.Equal(local, published) {
			return fmt.Errorf("installed package file differs from npm: %s", name)
		}
	}
	if !seen["package.json"] || !seen["openclaw.plugin.json"] || !seen["dist/index.js"] {
		return errors.New("published plugin runtime files are missing")
	}
	return nil
}
