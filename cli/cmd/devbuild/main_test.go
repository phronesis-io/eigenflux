package main

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/skills"
)

func TestZipDirectoryPreservesExecutableModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable modes are produced by the macOS bundle builder")
	}
	directory := t.TempDir()
	modes := map[string]os.FileMode{
		"eigenflux": 0755,
		"EigenFlux Notifications.app/Contents/MacOS/EigenFluxNotifications": 0755,
		"EigenFlux Notifications.app/Contents/Info.plist":                   0644,
	}
	for name, mode := range modes {
		path := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "test-bundle.zip")
	if err := zipDirectory(directory, archive); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.File) != len(modes) {
		t.Fatalf("unexpected ZIP entries: %d", len(reader.File))
	}
	for _, file := range reader.File {
		want, exists := modes[file.Name]
		if !exists || !file.Mode().IsRegular() || file.Mode().Perm() != want {
			t.Fatalf("ZIP entry %q has mode %v; want %v", file.Name, file.Mode(), want)
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(stream)
		stream.Close()
		if err != nil || string(content) != file.Name {
			t.Fatalf("ZIP entry %q lost content: %v", file.Name, err)
		}
	}
}

func TestPublicKeyRequired(t *testing.T) {
	for _, value := range []string{"", "garbage", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := decodePublicKey(value); err == nil {
			t.Fatalf("accepted invalid key %q", value)
		}
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePublicKey(base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
}

func TestPackagedSkillsVerifyAndRejectTampering(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	if err := writeFile(filepath.Join(root, "skills", "ef-broadcast", "SKILL.md"), []byte("test\r\ncontent\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(root, "skills", "ef-localdev", "SKILL.md"), []byte("excluded")); err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := skills.VerifyPublicKeyBase64
	skills.VerifyPublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { skills.VerifyPublicKeyBase64 = oldKey })
	if err := packageSkills(root, out, "1.0.0", "1.0.0", key); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(out, "cdn"))))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "installed")
	res, err := skills.Sync(skills.SyncOptions{Into: destination, CLIVersion: "1.0.0", CDNBase: server.URL})
	if err != nil || res == nil || !res.VerifiedManifest {
		t.Fatalf("signed bundle rejected: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "ef-localdev")); !os.IsNotExist(err) {
		t.Fatal("development-only Skill was packaged")
	}
	path := filepath.Join(out, "cdn", "skills", "latest", skills.RemoteManifest)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest skills.Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.MinCLIVersion = "0.0.1"
	if err := writeJSON(path, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := skills.Sync(skills.SyncOptions{Into: filepath.Join(t.TempDir(), "tampered"), CLIVersion: "1.0.0", CDNBase: server.URL}); err == nil {
		t.Fatal("tampered manifest accepted")
	}
}

func TestBuiltCLIUsesEmbeddedVerifier(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a native CLI and test source")
	}
	out := filepath.Join(t.TempDir(), "bundle with spaces")
	cmd := exec.Command("go", "run", ".", "-test-bundle", "-target", runtime.GOOS+"/"+runtime.GOARCH, "-out", out)
	cmd.Env = append(os.Environ(), "EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(out, "cdn"))))
	defer server.Close()
	home := filepath.Join(t.TempDir(), ".eigenflux")
	dest := filepath.Join(t.TempDir(), "skills")
	cli := exec.Command(filepath.Join(out, "eigenflux"+ext), "--homedir", home, "skills", "sync", "--into", dest, "--format", "json")
	cli.Env = append(os.Environ(), "EIGENFLUX_CDN_URL="+server.URL, "EIGENFLUX_SKILLS_DIR="+dest, "EIGENFLUX_HOME="+home)
	b, err := cli.CombinedOutput()
	if err != nil {
		t.Fatalf("built CLI failed signed sync: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Join(dest, skills.ManifestFileName)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "verifier is not configured") {
		t.Fatal("missing embedded trust root")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "private") || strings.HasSuffix(entry.Name(), ".pem") {
			t.Fatal("private key entered bundle")
		}
	}
	if _, err := os.Stat(out + ".zip"); err != nil {
		t.Fatal(err)
	}
}
