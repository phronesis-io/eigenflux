package pluginupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	o                            Options
	i                            inspection
	g                            gatewayStatus
	r                            runtimeStatus
	root, target                 string
	files                        map[string][]byte
	updates, restarts, refreshes int
	autoRestart                  bool
	autoRefresh                  bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{root: t.TempDir(), target: "0.0.46", files: map[string][]byte{"package.json": []byte(`{"version":"0.0.46"}`), "openclaw.plugin.json": []byte(`{"id":"openclaw-eigenflux"}`), "dist/index.js": []byte("official runtime")}}
	i := &f.i
	i.Plugin.ID, i.Plugin.PackageName, i.Plugin.Version, i.Plugin.RootDir = PluginID, PackageName, "0.0.46", f.root
	i.Plugin.Enabled, i.Plugin.Status = true, "loaded"
	i.Plugin.Trust.Reason, i.Plugin.Trust.RegistryPath = "provenance-invalid", filepath.Join(f.root, "openclaw.sqlite")
	i.Install.Source, i.Install.Spec, i.Install.InstallPath, i.Install.Version = "npm", PackageName+"@latest", f.root, "0.0.46"
	i.Install.ResolvedName, i.Install.ResolvedVersion, i.Install.ResolvedSpec = PackageName, "0.0.46", PackageName+"@0.0.46"
	for name, data := range f.files {
		p := filepath.Join(f.root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var archive bytes.Buffer
	z := gzip.NewWriter(&archive)
	tw := tar.NewWriter(z)
	for name, data := range f.files {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = z.Close()
	sum := sha512.Sum512(archive.Bytes())
	i.Install.Integrity = "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/artifact.tgz" {
			_, _ = w.Write(archive.Bytes())
			return
		}
		version := f.target
		if !strings.HasSuffix(r.URL.Path, "/latest") {
			version = filepath.Base(r.URL.Path)
		}
		meta := release{Name: PackageName, Version: version}
		meta.OpenClaw.Compat.MinGatewayVersion = "2026.5.2"
		meta.Dist.Tarball = "http://" + r.Host + "/artifact.tgz"
		meta.Dist.Integrity = i.Install.Integrity
		_ = json.NewEncoder(w).Encode(meta)
	}))
	t.Cleanup(server.Close)
	f.o.Registry = server.URL
	f.o.Client = server.Client()
	f.o.PollInterval = time.Millisecond
	f.g.CLI.Version, f.g.Gateway.Version = "2026.9.4", "2026.9.4"
	f.g.Service.Runtime.PID, f.g.Service.Runtime.Status = 100, "running"
	f.g.Config.CLI.Path, f.g.Config.Daemon.Path = "/openclaw/config", "/openclaw/config"
	f.g.RPC.OK = true
	f.g.Gateway.ProbeURL, f.g.RPC.URL = "ws://127.0.0.1:18789", "ws://127.0.0.1:18789"
	if err := json.Unmarshal([]byte(`{"schema_version":"eigenflux_runtime.v1","plugin_version":"0.0.46","gateway_pid":100,"servers":[{"server":"alpha","cli_version":"1.0.0","running":true,"ready":true}]}`), &f.r); err != nil {
		t.Fatal(err)
	}
	f.o.Server, f.o.CLIVersion = "alpha", "1.0.0"
	f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "plugins list --json":
			return []byte(`{"plugins":[{"id":"openclaw-eigenflux"}]}`), nil
		case "plugins inspect " + PluginID + " --json":
			return json.Marshal(f.i)
		case "gateway status --json":
			return json.Marshal(f.g)
		case "gateway call eigenflux.runtime --json --port 18789":
			return json.Marshal(f.r)
		case "plugins update " + PluginID + " --dry-run":
			if f.i.Plugin.Version == f.target {
				return []byte(fmt.Sprintf("%s is up to date (%s).", PluginID, f.target)), nil
			}
			return []byte(fmt.Sprintf("Would update %s: %s -> %s.", PluginID, f.i.Plugin.Version, f.target)), nil
		case "gateway call plugins.refresh --json --port 18789":
			f.refreshes++
			if f.autoRefresh {
				f.r.PluginVersion = f.target
				f.r.GatewayPID++
				f.g.Service.Runtime.PID = f.r.GatewayPID
			}
			return []byte(`{"ok":true,"restartRequired":true}`), nil
		case "plugins update " + PluginID:
			f.updates++
			f.i.Plugin.Version = f.target
			f.i.Install.Version = f.target
			f.i.Install.ResolvedVersion = f.target
			f.i.Install.ResolvedSpec = PackageName + "@" + f.target
			if f.autoRestart {
				f.r.PluginVersion = f.target
				f.r.GatewayPID++
				f.g.Service.Runtime.PID = f.r.GatewayPID
			}
			return nil, nil
		case "gateway restart":
			f.restarts++
			f.r.PluginVersion = f.target
			f.r.GatewayPID++
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected host command %q", args)
		}
	}
	return f
}

func TestThirdPartyCatalogLabelIsVerifiedAgainstPublisher(t *testing.T) {
	f := newFixture(t)
	r := Update(context.Background(), f.o)
	if r.Status != "loaded" || r.LoadedVersion != "0.0.46" || f.updates != 0 || f.restarts != 0 {
		t.Fatalf("intact third-party install blocked or mutated: %+v", r)
	}
}

func TestAbsentPluginIsNotInstalled(t *testing.T) {
	f := newFixture(t)
	f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "plugins list --json" {
			return []byte(`{"plugins":[]}`), nil
		}
		t.Fatalf("absent plugin triggered host command %q", args)
		return nil, nil
	}
	r := Update(context.Background(), f.o)
	if r.Status != "not_installed" || f.updates != 0 || f.restarts != 0 {
		t.Fatalf("absent plugin changed: %+v", r)
	}
}

func TestSourceAndFileFailuresNeverUpdateOrRestart(t *testing.T) {
	for _, kind := range []string{"package", "spec", "path", "integrity", "file", "symlink", "disabled", "remote_gateway", "busy", "downgrade"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "package":
				f.i.Install.ResolvedName = "@someone/other"
			case "spec":
				f.i.Install.Spec = "https://untrusted.example/plugin.tgz"
			case "path":
				f.i.Install.InstallPath = t.TempDir()
			case "integrity":
				f.i.Install.Integrity = "sha512-wrong"
			case "file":
				_ = os.WriteFile(filepath.Join(f.root, "dist/index.js"), []byte("edited"), 0600)
			case "symlink":
				p := filepath.Join(f.root, "dist/index.js")
				_ = os.Remove(p)
				_ = os.Symlink(filepath.Join(f.root, "package.json"), p)
			case "disabled":
				f.i.Plugin.Enabled = false
			case "remote_gateway":
				f.g.Config.Daemon.Path = "/remote/config"
			case "busy":
				f.r.Busy = true
			case "downgrade":
				f.target = "0.0.45"
			}
			r := Update(context.Background(), f.o)
			if r.Status != "blocked" || r.Error == "" || f.updates != 0 || f.restarts != 0 {
				t.Fatalf("unsafe install mutated: %+v updates=%d restarts=%d", r, f.updates, f.restarts)
			}
		})
	}
}

func TestStaleRuntimeDoesNotForcePendingHostRestart(t *testing.T) {
	f := newFixture(t)
	f.r.PluginVersion = "0.0.45"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	r := Update(ctx, f.o)
	if r.Status != "restart_required" || f.restarts != 0 || f.updates != 0 {
		t.Fatalf("deferred restart was forced: %+v", r)
	}
}

func TestHostAutomaticRestartIsNotRepeated(t *testing.T) {
	f := newFixture(t)
	f.i.Plugin.Version = "0.0.45"
	f.i.Install.Version = "0.0.45"
	f.i.Install.ResolvedVersion = "0.0.45"
	f.i.Install.ResolvedSpec = PackageName + "@0.0.45"
	f.r.PluginVersion = "0.0.45"
	f.autoRestart = true
	r := Update(context.Background(), f.o)
	if r.Status != "loaded" || f.updates != 1 || f.restarts != 0 {
		t.Fatalf("adoption failed: %+v updates=%d restarts=%d", r, f.updates, f.restarts)
	}
}

func TestDeferredHostRestartIsNeverForced(t *testing.T) {
	f := newFixture(t)
	f.i.Plugin.Version = "0.0.45"
	f.i.Install.Version = "0.0.45"
	f.i.Install.ResolvedVersion = "0.0.45"
	f.i.Install.ResolvedSpec = PackageName + "@0.0.45"
	f.r.PluginVersion = "0.0.45"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	r := Update(ctx, f.o)
	if r.Status != "restart_required" || f.updates != 1 || f.restarts != 0 {
		t.Fatalf("accepted host restart was raced: %+v", r)
	}
}

func TestLoadedModuleWithoutReadyWatchIsNotSuccess(t *testing.T) {
	for _, kind := range []string{"empty", "not_ready", "wrong_server", "old_cli"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "empty":
				f.r.Servers = nil
			case "not_ready":
				f.r.Servers[0].Ready = false
			case "wrong_server":
				f.r.Servers[0].Server = "another"
			case "old_cli":
				f.r.Servers[0].CLIVersion = "0.0.62"
			}
			r := Update(context.Background(), f.o)
			if r.Status != "restart_required" || r.LoadedVersion != "" || f.restarts != 0 {
				t.Fatalf("unready CLI was accepted: %+v", r)
			}
		})
	}
}

func TestOldAdapterDoesNotClaimLoadedOrRestartEveryCheck(t *testing.T) {
	f := newFixture(t)
	f.r.Schema = "old"
	r := Update(context.Background(), f.o)
	if r.Status != "restart_required" || r.LoadedVersion != "" || f.restarts != 0 || f.refreshes != 0 || r.RestartPendingVersion != "" {
		t.Fatalf("unproven adoption claimed: %+v", r)
	}
}

func TestRuntimeTimeoutRemainsRestartRequired(t *testing.T) {
	f := newFixture(t)
	f.r.PluginVersion = "0.0.45"
	base := f.o.Run
	f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "gateway restart" {
			f.restarts++
			return nil, nil
		}
		return base(ctx, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	r := Update(ctx, f.o)
	if r.Status != "restart_required" || r.LoadedVersion != "" || f.restarts != 0 {
		t.Fatalf("timeout lost pending adoption: %+v", r)
	}
}

func TestMissingRestartNotificationIsRepairedThroughManager(t *testing.T) {
	f := newFixture(t)
	f.r.PluginVersion = "0.0.45"
	f.autoRefresh = true
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := Update(ctx, f.o)
	if r.Status != "loaded" || f.updates != 0 || f.refreshes != 1 || f.restarts != 0 {
		t.Fatalf("lost notification was not repaired by the manager: %+v refreshes=%d", r, f.refreshes)
	}
}

func TestDeferredRestartGetsAtMostOneMetadataNotification(t *testing.T) {
	f := newFixture(t)
	f.r.PluginVersion = "0.0.45"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := Update(ctx, f.o)
	if r.Status != "restart_required" || f.refreshes != 1 || f.restarts != 0 {
		t.Fatalf("pending restart was repeatedly requested or forced: %+v refreshes=%d", r, f.refreshes)
	}
}

func TestWatchReadinessDoesNotRequestMetadataRestart(t *testing.T) {
	f := newFixture(t)
	f.r.Servers[0].Ready = false
	r := Update(context.Background(), f.o)
	if r.Status != "restart_required" || f.refreshes != 0 || f.restarts != 0 {
		t.Fatalf("watch readiness caused a Gateway restart: %+v", r)
	}
}

func TestRuntimeMustBelongToCurrentLocalGateway(t *testing.T) {
	for _, kind := range []string{"other_pid", "remote_url", "different_url", "missing_url"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "other_pid":
				f.r.GatewayPID = 900
			case "remote_url":
				f.g.Gateway.ProbeURL, f.g.RPC.URL = "ws://remote.example:18789", "ws://remote.example:18789"
			case "different_url":
				f.g.RPC.URL = "ws://127.0.0.1:28789"
			case "missing_url":
				f.g.Gateway.ProbeURL, f.g.RPC.URL = "", ""
			}
			r := Update(context.Background(), f.o)
			if r.Status != "blocked" || f.updates != 0 || f.refreshes != 0 {
				t.Fatalf("unbound runtime accepted: %+v", r)
			}
		})
	}
}

func TestChangedPIDWithoutRuntimeAdoptionDoesNotRequestRestart(t *testing.T) {
	f := newFixture(t)
	f.r.PluginVersion = "0.0.45"
	base := f.o.Run
	reads := 0
	f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "gateway status --json" {
			reads++
			if reads > 1 {
				f.g.Service.Runtime.PID = 101
				f.r.GatewayPID = 101
			}
		}
		return base(ctx, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := Update(ctx, f.o)
	if r.Status != "restart_required" || f.refreshes != 0 || f.restarts != 0 {
		t.Fatalf("another PID was unnecessarily restarted: %+v", r)
	}
}

func TestHostCompatibleSelectionReplacesRegistryLatestDiscovery(t *testing.T) {
	f := newFixture(t)
	f.i.Plugin.Version, f.i.Install.Version, f.i.Install.ResolvedVersion, f.i.Install.ResolvedSpec = "0.0.45", "0.0.45", "0.0.45", PackageName+"@0.0.45"
	f.r.PluginVersion = "0.0.45"
	f.autoRestart = true
	base := f.o.Client.Transport
	f.o.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/latest") {
			t.Fatal("updater bypassed host compatible-version resolution")
		}
		resp, err := base.RoundTrip(req)
		if err == nil && !strings.HasSuffix(req.URL.Path, "/artifact.tgz") {
			var m map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&m)
			_ = resp.Body.Close()
			m["OpenClaw"] = map[string]any{"compat": map[string]any{"pluginApi": ">=2026.5.2"}}
			b, _ := json.Marshal(m)
			resp.Body = io.NopCloser(bytes.NewReader(b))
		}
		return resp, err
	})
	r := Update(context.Background(), f.o)
	if r.Status != "loaded" || r.LatestVersion != f.target || f.updates != 1 {
		t.Fatalf("compatible selection rejected: %+v", r)
	}
}

func TestPinnedInstallDoesNotDiscoverOrReplaceSelector(t *testing.T) {
	f := newFixture(t)
	f.i.Install.Spec = PackageName + "@0.0.46"
	f.target = "0.0.47"
	base := f.o.Run
	f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.HasPrefix(strings.Join(args, " "), "plugins update") {
			t.Fatal("pinned install was sent to update discovery")
		}
		return base(ctx, args...)
	}
	r := Update(context.Background(), f.o)
	if r.Status != "loaded" || r.LatestVersion != "0.0.46" || f.updates != 0 {
		t.Fatalf("pin was replaced: %+v", r)
	}
}

func TestUnsupportedHostDiscoveryBlocksBeforeMutation(t *testing.T) {
	for _, output := range []string{"", "Skipping unsupported plugin.", "Would update openclaw-eigenflux: 0.0.46 -> 0.0.47-beta.1."} {
		t.Run(output, func(t *testing.T) {
			f := newFixture(t)
			base := f.o.Run
			f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
				if strings.Join(args, " ") == "plugins update "+PluginID+" --dry-run" {
					return []byte(output), nil
				}
				return base(ctx, args...)
			}
			r := Update(context.Background(), f.o)
			if r.Status != "blocked" || f.updates != 0 || f.refreshes != 0 {
				t.Fatalf("unknown discovery permitted mutations: %+v", r)
			}
		})
	}
}

func TestRefreshFailureIsRecordedWithoutRepeatedRequests(t *testing.T) {
	f := newFixture(t)
	f.r.PluginVersion = "0.0.45"
	base := f.o.Run
	f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "gateway call plugins.refresh --json --port 18789" {
			f.refreshes++
			return nil, fmt.Errorf("host RPC unavailable")
		}
		return base(ctx, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := Update(ctx, f.o)
	if r.Status != "restart_required" || f.refreshes != 1 || !strings.Contains(r.Error, "host RPC unavailable") {
		t.Fatalf("notification failure lost or repeated: %+v", r)
	}
}

func TestForeignRuntimeCannotAttestPostUpdateAdoption(t *testing.T) {
	f := newFixture(t)
	f.i.Plugin.Version, f.i.Install.Version, f.i.Install.ResolvedVersion, f.i.Install.ResolvedSpec = "0.0.45", "0.0.45", "0.0.45", PackageName+"@0.0.45"
	f.r.PluginVersion = "0.0.45"
	base := f.o.Run
	f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if f.updates > 0 {
			f.r.PluginVersion, f.r.GatewayPID = "0.0.46", 900
		}
		return base(ctx, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := Update(ctx, f.o)
	if r.Status != "restart_required" || r.LoadedVersion != "" || f.refreshes != 0 {
		t.Fatalf("foreign Gateway proved local adoption: %+v", r)
	}
}

func TestHostDiscoveryAcceptsColorsPreambleAndSameVersion(t *testing.T) {
	for _, output := range []string{
		"[plugins] discovered plugins\nopenclaw-eigenflux is up to date (0.0.46).\n",
		"[plugins] checking compatibility\r\n\x1b[32mWould update openclaw-eigenflux: 0.0.46 -> 0.0.47.\x1b[0m\r\n",
	} {
		f := newFixture(t)
		f.o.Run = func(context.Context, ...string) ([]byte, error) { return []byte(output), nil }
		got, err := discoverVersion(context.Background(), f.o, f.i)
		if err != nil || (got != "0.0.46" && got != "0.0.47") {
			t.Fatalf("host output rejected: %q version=%s err=%v", output, got, err)
		}
	}
}

func TestFirstRuntimeAdapterInstallPersistsAndRepairsLostNotification(t *testing.T) {
	f := newFixture(t)
	f.i.Plugin.Version, f.i.Install.Version, f.i.Install.ResolvedVersion, f.i.Install.ResolvedSpec = "0.0.45", "0.0.45", "0.0.45", PackageName+"@0.0.45"
	f.r.PluginVersion, f.r.Schema = "0.0.45", "old"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	r := Update(ctx, f.o)
	cancel()
	if r.Status != "restart_required" || r.RestartPendingVersion != "0.0.46" || f.updates != 1 || f.refreshes != 1 {
		t.Fatalf("first adapter adoption evidence lost: %+v", r)
	}
	f.o.RestartPendingVersion = r.RestartPendingVersion
	f.autoRefresh = true
	base := f.o.Run
	f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "gateway call plugins.refresh --json --port 18789" {
			f.r.Schema = "eigenflux_runtime.v1"
		}
		return base(ctx, args...)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r = Update(ctx, f.o)
	if r.Status != "loaded" || r.RestartPendingVersion != "" || f.updates != 1 || f.refreshes != 2 || f.restarts != 0 {
		t.Fatalf("pending first adapter adoption not recovered: %+v", r)
	}
}

func TestStalePendingTargetDoesNotRestartAdapterWithoutRuntime(t *testing.T) {
	f := newFixture(t)
	f.r.Schema = "old"
	f.o.RestartPendingVersion = "0.0.47"
	r := Update(context.Background(), f.o)
	if r.Status != "restart_required" || r.RestartPendingVersion != "" || f.refreshes != 0 {
		t.Fatalf("stale pending evidence restarted an old adapter: %+v", r)
	}
}

func TestPendingTargetDoesNotRestartChangedPIDWithoutRuntime(t *testing.T) {
	f := newFixture(t)
	f.r.Schema = "old"
	f.o.RestartPendingVersion = "0.0.46"
	base := f.o.Run
	statusReads := 0
	f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "gateway status --json" {
			statusReads++
			if statusReads > 1 {
				f.g.Service.Runtime.PID = 101
			}
		}
		return base(ctx, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := Update(ctx, f.o)
	if r.Status != "restart_required" || r.RestartPendingVersion != "0.0.46" || f.refreshes != 0 {
		t.Fatalf("new Gateway PID was restarted without runtime evidence: %+v", r)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
