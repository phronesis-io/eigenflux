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
	if *testBundle {
		if err := packageSkills(root, *out, version, minimum, private); err != nil {
			return err
		}
		name := "eigenflux-" + parts[0] + "-" + parts[1] + ext
		data, err := os.ReadFile(binary)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		m := selfupdate.Manifest{Version: version, Artifacts: map[string]selfupdate.Artifact{name: {SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}}}
		if err := selfupdate.Sign(&m, private); err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(*out, "cdn", "cli", "latest", "release.json"), m); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(*out, "cdn", "cli", version, name), data); err != nil {
			return err
		}
		if err := build(root, *target, filepath.Join(*out, "test-source"+ext), "./cmd/devbuild", ""); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(*out, "Run-Test.ps1"), testRunner); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(*out, "TEST-ONLY.txt"), []byte("TEST ONLY. This CLI trusts only its paired local Skills/CDN. Do not install it over a production CLI. Run-Test.ps1 uses isolated state and does not change PATH. Private signing keys are not included.\n")); err != nil {
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
	if *testBundle {
		fmt.Println("PowerShell: .\\Run-Test.ps1 version; .\\Run-Test.ps1 skills sync; .\\Run-Test.ps1 heartbeat plan")
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

func zipDirectory(dir, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	z := zip.NewWriter(f)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		w, err := z.Create(filepath.ToSlash(rel))
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
