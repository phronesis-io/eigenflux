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
)

type fixture struct {
	o                   Options
	i                   inspection
	g                   gatewayStatus
	root, target        string
	files               map[string][]byte
	updates             int
	restarts, refreshes int
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
	f.g.CLI.Version, f.g.Gateway.Version = "2026.9.4", "2026.9.4"
	f.g.Service.Runtime.PID, f.g.Service.Runtime.Status = 100, "running"
	f.g.Config.CLI.Path, f.g.Config.Daemon.Path = "/openclaw/config", "/openclaw/config"
	f.g.RPC.OK = true
	f.g.Gateway.ProbeURL, f.g.RPC.URL = "ws://127.0.0.1:18789", "ws://127.0.0.1:18789"
	f.o.LoadedVersion = "0.0.46"
	f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "plugins list --json":
			return []byte(`{"plugins":[{"id":"openclaw-eigenflux"}]}`), nil
		case "plugins inspect " + PluginID + " --json":
			return json.Marshal(f.i)
		case "gateway status --json":
			return json.Marshal(f.g)
		case "plugins update " + PluginID + " --dry-run":
			if f.i.Plugin.Version == f.target {
				return []byte(fmt.Sprintf("%s is up to date (%s).", PluginID, f.target)), nil
			}
			return []byte(fmt.Sprintf("Would update %s: %s -> %s.", PluginID, f.i.Plugin.Version, f.target)), nil
		case "plugins update " + PluginID:
			f.updates++
			f.i.Plugin.Version = f.target
			f.i.Install.Version = f.target
			f.i.Install.ResolvedVersion = f.target
			f.i.Install.ResolvedSpec = PackageName + "@" + f.target
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
	for _, kind := range []string{"package", "spec", "path", "integrity", "file", "symlink", "disabled", "remote_gateway", "downgrade"} {
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

func TestPublishedPluginVersionConfirmsOnlyCallingPlugin(t *testing.T) {
	for _, loaded := range []string{"", "0.0.45", "0.0.47"} {
		t.Run(loaded, func(t *testing.T) {
			f := newFixture(t)
			f.o.LoadedVersion = loaded
			r := Update(context.Background(), f.o)
			if r.Status != "restart_required" || r.LoadedVersion != "" || f.updates != 0 {
				t.Fatalf("unproven running version accepted: %+v", r)
			}
		})
	}
}

func TestUpdateWaitsForLaterPublishedPluginHeartbeat(t *testing.T) {
	f := newFixture(t)
	f.i.Plugin.Version, f.i.Install.Version, f.i.Install.ResolvedVersion, f.i.Install.ResolvedSpec = "0.0.45", "0.0.45", "0.0.45", PackageName+"@0.0.45"
	f.o.LoadedVersion = "0.0.45"
	r := Update(context.Background(), f.o)
	if r.Status != "restart_required" || r.LoadedVersion != "" || f.updates != 1 {
		t.Fatalf("installation falsely attested loading: %+v", r)
	}
	// Even a manager restart during update cannot make the inherited environment
	// establish which package is loaded in the replacement process.
	f.g.Service.Runtime.PID++
	r = Update(context.Background(), f.o)
	if r.Status != "restart_required" || r.LoadedVersion != "" || f.updates != 1 {
		t.Fatalf("old worker or PID attested replacement: %+v", r)
	}
	f.o.LoadedVersion = f.target
	r = Update(context.Background(), f.o)
	if r.Status != "loaded" || r.LoadedVersion != f.target || f.updates != 1 {
		t.Fatalf("fresh published plugin heartbeat not accepted: %+v", r)
	}
}

func TestUpdaterNeverCallsUnpublishedRPCOrForcesRestart(t *testing.T) {
	f := newFixture(t)
	f.i.Plugin.Version, f.i.Install.Version, f.i.Install.ResolvedVersion, f.i.Install.ResolvedSpec = "0.0.45", "0.0.45", "0.0.45", PackageName+"@0.0.45"
	base := f.o.Run
	f.o.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "gateway" && args[1] != "status" {
			t.Fatalf("unsupported runtime call or forced restart: %q", args)
		}
		return base(ctx, args...)
	}
	r := Update(context.Background(), f.o)
	if r.Status != "restart_required" || f.updates != 1 {
		t.Fatalf("manager did not install: %+v", r)
	}
}

func TestLocalGatewayValidationBlocksMutation(t *testing.T) {
	for _, kind := range []string{"remote_url", "different_url", "missing_url", "config", "pid", "stopped"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "remote_url":
				f.g.Gateway.ProbeURL, f.g.RPC.URL = "ws://remote.example:18789", "ws://remote.example:18789"
			case "different_url":
				f.g.RPC.URL = "ws://127.0.0.1:28789"
			case "missing_url":
				f.g.Gateway.ProbeURL, f.g.RPC.URL = "", ""
			case "config":
				f.g.Config.Daemon.Path = "/other/config"
			case "pid":
				f.g.Service.Runtime.PID = 0
			case "stopped":
				f.g.Service.Runtime.Status = "stopped"
			}
			r := Update(context.Background(), f.o)
			if r.Status != "blocked" || f.updates != 0 {
				t.Fatalf("foreign or stopped Gateway accepted: %+v", r)
			}
		})
	}
}

func TestHostCompatibleSelectionReplacesRegistryLatestDiscovery(t *testing.T) {
	f := newFixture(t)
	f.i.Plugin.Version, f.i.Install.Version, f.i.Install.ResolvedVersion, f.i.Install.ResolvedSpec = "0.0.45", "0.0.45", "0.0.45", PackageName+"@0.0.45"
	f.o.LoadedVersion = "0.0.45"
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
	if r.Status != "restart_required" || r.LatestVersion != f.target || f.updates != 1 {
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpdateProgressDistinguishesInstallationFromLoadedRuntime(t *testing.T) {
	f := newFixture(t)
	f.i.Plugin.Version, f.i.Install.Version, f.i.Install.ResolvedVersion = "0.0.45", "0.0.45", "0.0.45"
	f.i.Install.ResolvedSpec = PackageName + "@0.0.45"
	// A stale or incorrect loaded marker must never attest a package installed
	// during this invocation, even if it happens to equal the target version.
	f.o.LoadedVersion = f.target
	var observations []Progress
	f.o.OnProgress = func(p Progress) { observations = append(observations, p) }
	r := Update(context.Background(), f.o)
	if r.Status != "restart_required" || r.Phase != "load" || r.LoadedVersion != "" {
		t.Fatalf("terminal evidence: %+v", r)
	}
	var stages []string
	for _, p := range observations {
		stages = append(stages, p.Phase+":"+p.Result)
		if p.Phase != "check" && (p.FromVersion != "0.0.45" || p.TargetVersion != f.target) {
			t.Fatalf("lost versions: %+v", p)
		}
	}
	if got := strings.Join(stages, ","); got != "check:started,verify:started,install:started,install:installed,load:started" {
		t.Fatalf("stages=%s", got)
	}
}

func TestUpdateProgressPreservesSourceFailurePhaseAndDiscoveredTarget(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "dist/index.js"), []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	var observations []Progress
	f.o.OnProgress = func(p Progress) { observations = append(observations, p) }
	r := Update(context.Background(), f.o)
	if r.Status != "blocked" || r.Phase != "verify" || r.LatestVersion != f.target || f.updates != 0 {
		t.Fatalf("source failure mislabeled: %+v", r)
	}
	if len(observations) != 2 || observations[1].Phase != "verify" || observations[1].TargetVersion != f.target {
		t.Fatalf("source verification observation=%+v", observations)
	}
}

func TestUpdateProgressPanicCannotInterruptInstallationOrRuntimeCheck(t *testing.T) {
	f := newFixture(t)
	f.i.Plugin.Version, f.i.Install.Version, f.i.Install.ResolvedVersion = "0.0.45", "0.0.45", "0.0.45"
	f.i.Install.ResolvedSpec = PackageName + "@0.0.45"
	f.o.LoadedVersion = "0.0.45"
	f.o.OnProgress = func(Progress) { panic("observer failed") }
	result := Update(context.Background(), f.o)
	if result.Status != "restart_required" || result.LoadedVersion != "" || f.updates != 1 {
		t.Fatalf("observer interrupted plugin maintenance: %+v updates=%d", result, f.updates)
	}
}
