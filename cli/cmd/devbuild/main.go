// devbuild builds a CLI with an explicit trust root, or a self-contained test
// bundle. Test signing keys live only in memory and never enter the output.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"
	"cli.eigenflux.ai/internal/uninstallscript"
)

//go:embed run-test.ps1.txt
var testRunner []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	testBundle := flag.Bool("test-bundle", false, "generate an isolated, signed test bundle (not a production release)")
	target := flag.String("target", "windows/"+runtime.GOARCH, "target OS/architecture")
	out := flag.String("out", "", "new output directory, relative to the repository root")
	keyText := flag.String("public-key", os.Getenv("EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY"), "trusted Ed25519 public key in base64")
	serve := flag.String("serve", "", "serve this test bundle's CDN directory on loopback")
	ready := flag.String("ready-file", "", "write the loopback address to this file")
	flag.Parse()
	if *serve != "" {
		return serveBundle(*serve, *ready)
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	parts := strings.Split(*target, "/")
	if len(parts) != 2 || (parts[0] != "windows" && parts[0] != "darwin" && parts[0] != "linux") || (parts[1] != "amd64" && parts[1] != "arm64") {
		return fmt.Errorf("unsupported target %q", *target)
	}
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	config, err := os.ReadFile(filepath.Join(root, "cli", ".cli.config"))
	if err != nil {
		return err
	}
	version := configValue(string(config), "CLI_VERSION")
	minimum := configValue(string(config), "SKILLS_MIN_CLI_VERSION")
	if !selfupdate.ValidVersion(version) || !selfupdate.ValidVersion(minimum) {
		return fmt.Errorf("invalid CLI version configuration")
	}
	commitBytes, err := exec.Command("git", "-C", root, "rev-parse", "--short=8", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("read git commit: %w", err)
	}
	commit := strings.TrimSpace(string(commitBytes))
	status, err := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=normal").Output()
	if err != nil {
		return err
	}
	if len(status) > 0 {
		commit += "-dirty"
	}
	var private ed25519.PrivateKey
	if *testBundle {
		if strings.TrimSpace(*keyText) != "" {
			return fmt.Errorf("test bundles generate their own key; omit -public-key and unset EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY")
		}
		public, generated, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		private = generated
		*keyText = base64.StdEncoding.EncodeToString(public)
	}
	public, err := decodePublicKey(*keyText)
	if err != nil {
		return err
	}
	*keyText = base64.StdEncoding.EncodeToString(public)
	if *out == "" {
		*out = filepath.Join("build", "windows-"+version+"-"+parts[1]+"-"+time.Now().Format("20060102-150405"))
	}
	if !filepath.IsAbs(*out) {
		*out = filepath.Join(root, *out)
	}
	if _, err := os.Lstat(*out); !os.IsNotExist(err) {
		return fmt.Errorf("output must be a new directory: %s", *out)
	}
	if err := os.MkdirAll(*out, 0700); err != nil {
		return err
	}
	ext := ""
	if parts[0] == "windows" {
		ext = ".exe"
	}
	binary := filepath.Join(*out, "eigenflux"+ext)
	ldflags := "-X main.Version=" + version + " -X main.Commit=" + commit + " -X cli.eigenflux.ai/internal/skills.VerifyPublicKeyBase64=" + *keyText
	if err := build(root, *target, binary, ".", ldflags); err != nil {
		return err
	}
	for name, data := range map[string][]byte{"uninstall.sh": uninstallscript.POSIX, "uninstall.ps1": uninstallscript.Windows} {
		if err := writeFile(filepath.Join(*out, name), data); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(*out, name), 0755); err != nil {
			return err
		}
	}
	if parts[0] == "darwin" {
		command := exec.Command("bash", filepath.Join(root, "cli", "scripts", "build-notifier-macos.sh"), *out, parts[1])
		command.Env = append(os.Environ(), "EIGENFLUX_NOTIFIER_VERSION="+version)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("build macOS notification helper: %w", err)
		}
	}
	if *testBundle {
		if err := packageSkills(root, *out, version, minimum, private); err != nil {
			return err
		}
		if err := packageCLI(*out, *target, version, private); err != nil {
			return err
		}
		if err := build(root, *target, filepath.Join(*out, "test-source"+ext), "./cmd/devbuild", ""); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(*out, "Run-Test.ps1"), testRunner); err != nil {
			return err
		}
		instructions := "TEST ONLY. This CLI trusts only its paired local Skills/CDN. Do not install it over a production CLI. Run-Test.ps1 uses isolated state and does not change PATH. Private signing keys are not included. Each build generates a fresh trust root; its signed complete package supports this bundle's bootstrap, not automatic tracking of later Git commits or independently built test bundles.\n"
		if parts[0] == "darwin" {
			instructions += "macOS source upgrades: use cli/scripts/install-test-macos.sh with the existing --homedir and current --host. It installs the complete paired package and runs notify setup/status automatically; building or copying a standalone CLI is not an installation.\n"
			instructions += "macOS: keep EigenFlux Notifications.app beside the real eigenflux executable. From this directory, run ./eigenflux notify setup, then ./eigenflux notify status. Building or syncing Skills alone does not request permission. macOS prompts only on first request; if denied, enable EigenFlux in System Settings > Notifications. After updating the existing wrapper to this complete package, run ./eigenflux watch restart --launch-agent <absolute-plist-path-in-your-Library/LaunchAgents>. Run from a separate foreground session; preserve the original account parameters.\n"
		}
		if parts[0] == "windows" {
			instructions += "Windows: run .\\Run-Test.ps1 notify setup and .\\Run-Test.ps1 notify status. Setup registers silently; Windows has no Allow permission dialog. Registered/pending_first_notification means the setting is unknown until the first real message. Read local observations with .\\Run-Test.ps1 notify log. Only explicitly run notify test --url https://www.eigenflux.ai if you want a visible test notification. If disabled, check Settings > System > Notifications. OS submission does not prove banner visibility.\n"
			instructions += "Windows WorkBuddy: keep the signed-in Desktop host open. To reuse an authenticated account, pass its exact --homedir and --server to BOTH Run-Test.ps1 skills sync and Run-Test.ps1 watch start --host workbuddy. This discovers the host, pins the binding, and migrates the owned notification task automatically; do not hand-write binding JSON or edit the task. Keep this complete package in a permanent path. Check watch status runtime.running/runtime.ready and verify a real approved order separately. Windows native acceptance is pending.\n"
			instructions += "After updating the existing watch task/wrapper to this complete package, run .\\Run-Test.ps1 watch restart --scheduled-task <exact-existing-task-path> from a separate foreground session. The task must run as the current interactive user without elevation.\n"
		}
		instructions += "Restart reports process_started with runtime_verified:false. Confirm a fresh runtime_ready in watch logs, notification status, and an actual message separately. Building or syncing Skills alone never restarts a background task.\n"
		if err := writeFile(filepath.Join(*out, "TEST-ONLY.txt"), []byte(instructions)); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if err := writeJSON(filepath.Join(*out, "build-info.json"), map[string]interface{}{"version": version, "commit": commit, "target": *target, "test_only": *testBundle, "public_key": *keyText, "sha256": hex.EncodeToString(sum[:])}); err != nil {
		return err
	}
	if err := zipDirectory(*out, *out+".zip"); err != nil {
		return err
	}
	fmt.Printf("Built %s\nZIP: %s.zip\n", binary, *out)
	if *testBundle && parts[0] == "windows" {
		fmt.Println("PowerShell: .\\Run-Test.ps1 version; .\\Run-Test.ps1 skills sync; .\\Run-Test.ps1 heartbeat plan")
		fmt.Println("WorkBuddy: with the same explicit --homedir/--server, run .\\Run-Test.ps1 skills sync, then .\\Run-Test.ps1 watch start --host workbuddy; no manual binding or task edit. Other hosts: after updating the existing background task's package path: .\\Run-Test.ps1 watch restart --scheduled-task <exact-existing-task-path>. Check a fresh runtime_ready in watch logs.")
	}
	if parts[0] == "darwin" {
		fmt.Println("macOS source installation: use cli/scripts/install-test-macos.sh --homedir <existing-agent-home> --host <current-host>. Building a bundle alone does not install it.")
		fmt.Println("macOS: from the output directory, run ./eigenflux notify setup, then ./eigenflux notify status. Keep EigenFlux Notifications.app beside the CLI. After updating the existing wrapper's package path, run ./eigenflux watch restart --launch-agent <absolute-plist-path> from a separate foreground session.")
	}
	return nil
}

func decodePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("a valid EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY is required; use -TestBundle for local testing without a production key")
	}
	return ed25519.PublicKey(b), nil
}

func repositoryRoot() (string, error) {
	b, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	return strings.TrimSpace(string(b)), err
}

func configValue(s, key string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+"="))
		}
	}
	return ""
}

func build(root, target, path, pkg, ldflags string) error {
	parts := strings.Split(target, "/")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", path, pkg)
	cmd.Dir = filepath.Join(root, "cli")
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "GOOS=") && !strings.HasPrefix(item, "GOARCH=") && !strings.HasPrefix(item, "CGO_ENABLED=") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env, "GOOS="+parts[0], "GOARCH="+parts[1], "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func packageSkills(root, out, version, minimum string, key ed25519.PrivateKey) error {
	src := filepath.Join(root, "skills")
	names, err := skills.DiscoverProductionSkills(src)
	if err != nil {
		return err
	}
	dir := filepath.Join(out, "cdn", "skills", "latest")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tarPath := filepath.Join(dir, skills.TarName)
	f, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		err = filepath.WalkDir(filepath.Join(src, name), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Name() == ".DS_Store" || strings.HasPrefix(d.Name(), "._") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				return fmt.Errorf("unsupported Skills file: %s", path)
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			h, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			h.Name, h.ModTime, h.Uid, h.Gid = filepath.ToSlash(rel), time.Unix(1577836800, 0), 0, 0
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, err = tw.Write(data)
			return err
		})
		if err != nil {
			break
		}
	}
	for _, closer := range []io.Closer{tw, gz, f} {
		if closeErr := closer.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return err
	}
	m, err := skills.GenerateManifest(src, version, minimum, names, time.Now().Unix())
	if err != nil {
		return err
	}
	m.Sequence = uint64(time.Now().UnixNano())
	m.TarSHA256, err = skills.TarballSHA256(tarPath)
	if err != nil {
		return err
	}
	if err := skills.SignManifest(m, key); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, skills.RemoteManifest), m); err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, skills.TarSHAName), []byte(m.TarSHA256+"\n"))
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func writeJSON(path string, value interface{}) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(b, '\n'))
}

// packageCLI signs only the selected executable/helper pair. The distribution's
// loopback server, wrappers and Skills remain outside the updater payload.
func packageCLI(out, target, version string, key ed25519.PrivateKey) error {
	parts := strings.Split(target, "/")
	if len(parts) != 2 || (parts[0] != "darwin" && parts[0] != "windows" && parts[0] != "linux") || (parts[1] != "amd64" && parts[1] != "arm64") || !selfupdate.ValidVersion(version) || len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid test CLI package configuration")
	}
	executable, name := "eigenflux", "eigenflux-"+parts[0]+"-"+parts[1]
	if parts[0] == "windows" {
		executable += ".exe"
		name += ".exe"
	}
	info, err := os.Lstat(filepath.Join(out, executable))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 128<<20 {
		return fmt.Errorf("invalid test CLI executable")
	}
	if parts[0] == "darwin" {
		for _, relative := range []string{"EigenFlux Notifications.app", "EigenFlux Notifications.app/Contents/Info.plist", "EigenFlux Notifications.app/Contents/MacOS/EigenFluxNotifications"} {
			info, err := os.Lstat(filepath.Join(out, filepath.FromSlash(relative)))
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || (relative == "EigenFlux Notifications.app" && !info.IsDir()) || (relative != "EigenFlux Notifications.app" && !info.Mode().IsRegular()) {
				return fmt.Errorf("invalid paired macOS notification helper")
			}
		}
	}
	versionDir := filepath.Join(out, "cdn", "cli", version)
	if err := os.MkdirAll(versionDir, 0700); err != nil {
		return err
	}
	archive := filepath.Join(versionDir, name+".zip")
	if err := zipSelectedDirectory(out, archive, func(relative string) bool {
		return relative == executable || (parts[0] == "darwin" && (relative == "EigenFlux Notifications.app" || strings.HasPrefix(relative, "EigenFlux Notifications.app/")))
	}); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(out, executable))
	if err != nil {
		return err
	}
	packageData, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	if len(packageData) > 256<<20 {
		return fmt.Errorf("test CLI package exceeds size limit")
	}
	artifact := func(data []byte) selfupdate.Artifact {
		sum := sha256.Sum256(data)
		return selfupdate.Artifact{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	}
	manifest := selfupdate.Manifest{Version: version, Artifacts: map[string]selfupdate.Artifact{name: artifact(data)}, Packages: map[string]selfupdate.Artifact{name + ".zip": artifact(packageData)}}
	if err := writeFile(filepath.Join(versionDir, name), data); err != nil {
		return err
	}
	for _, filename := range []string{"package-release.json", "release.json"} {
		if filename == "release.json" {
			manifest.Packages = nil
		}
		if err := selfupdate.Sign(&manifest, key); err != nil {
			return err
		}
		for _, location := range []string{versionDir, filepath.Join(out, "cdn", "cli", "latest")} {
			if err := writeJSON(filepath.Join(location, filename), manifest); err != nil {
				return err
			}
		}
	}
	return nil
}

func zipDirectory(dir, path string) error {
	return zipSelectedDirectory(dir, path, nil)
}

func zipSelectedDirectory(dir, path string, include func(string) bool) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	z := zip.NewWriter(f)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if include != nil && !include(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported ZIP file: %s", rel)
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Deflate
		w, err := z.CreateHeader(header)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	if e := z.Close(); err == nil {
		err = e
	}
	if e := f.Close(); err == nil {
		err = e
	}
	return err
}

func serveBundle(dir, readyFile string) error {
	if readyFile == "" {
		return fmt.Errorf("-ready-file is required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	tmp, err := os.CreateTemp(filepath.Dir(readyFile), ".source-ready-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.WriteString("http://" + listener.Addr().String())
	closeErr := tmp.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmp.Name(), readyFile); err != nil {
		return err
	}
	server := &http.Server{Handler: http.FileServer(http.Dir(dir)), ReadHeaderTimeout: 5 * time.Second}
	return server.Serve(listener)
}
