package tests

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cli.eigenflux.ai/internal/skills"
)

func signedRelease(t *testing.T, key ed25519.PrivateKey, sequence uint64, content string) (*skills.Manifest, []byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ef-profile"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ef-profile", "SKILL.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := skills.GenerateManifest(root, "0.0.42", "0.0.39", []string{"ef-profile"}, 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gz)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "ef-profile/SKILL.md", Mode: 0600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	manifest.TarSHA256 = hex.EncodeToString(sum[:])
	manifest.Sequence = sequence
	if err := skills.SignManifest(manifest, key); err != nil {
		t.Fatal(err)
	}
	return manifest, buf.Bytes()
}

func TestSignedSequenceConflictAndAtomicRecovery(t *testing.T) {
	publicKey, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	previousKey := skills.VerifyPublicKeyBase64
	skills.VerifyPublicKeyBase64 = base64.StdEncoding.EncodeToString(publicKey)
	t.Cleanup(func() { skills.VerifyPublicKeyBase64 = previousKey })
	old, oldTar := signedRelease(t, key, 8, "old official content")
	conflict, conflictTar := signedRelease(t, key, 8, "new official content")
	recovered, recoveredTar := signedRelease(t, key, 9, "new official content")
	serve := func(manifest *skills.Manifest, archive []byte) *httptest.Server {
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/skills/latest/manifest.json", "/cli/latest/manifest.json":
				_, _ = w.Write(data)
			case "/skills/latest/skills.tar.gz", "/cli/latest/skills.tar.gz":
				_, _ = w.Write(archive)
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		return server
	}
	oldServer := serve(old, oldTar)
	conflictServer := serve(conflict, conflictTar)
	recoveryServer := serve(recovered, recoveredTar)
	target := filepath.Join(t.TempDir(), "skills")
	opts := skills.SyncOptions{Into: target, CLIVersion: "0.0.42", CDNBase: oldServer.URL}
	if _, err := skills.Sync(opts); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "user-skill"), 0700); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(target, "user-skill", "SKILL.md")
	if err := os.WriteFile(userFile, []byte("user content"), 0600); err != nil {
		t.Fatal(err)
	}
	opts.CDNBase = conflictServer.URL
	result, err := skills.Sync(opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "local" || result.Atomic || result.VerifiedManifest {
		t.Fatalf("conflict accepted: %+v", result)
	}
	got, err := os.ReadFile(filepath.Join(target, "ef-profile", "SKILL.md"))
	if err != nil || string(got) != "old official content" {
		t.Fatalf("conflict changed local content: %q %v", got, err)
	}
	opts.CDNBase = recoveryServer.URL
	result, err = skills.Sync(opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "skills/latest" || !result.Atomic || !result.VerifiedManifest || len(result.Preserved) != 0 {
		t.Fatalf("recovery failed: %+v", result)
	}
	got, err = os.ReadFile(filepath.Join(target, "ef-profile", "SKILL.md"))
	if err != nil || string(got) != "new official content" {
		t.Fatalf("recovery content: %q %v", got, err)
	}
	got, err = os.ReadFile(userFile)
	if err != nil || string(got) != "user content" {
		t.Fatalf("user skill changed: %q %v", got, err)
	}
	installed, err := skills.ReadLocalManifest(target)
	if err != nil || installed.Sequence != 9 {
		t.Fatalf("sequence not persisted: %+v %v", installed, err)
	}
	// Once 9 is accepted, even a valid old signature cannot roll it back.
	opts.CDNBase = oldServer.URL
	result, err = skills.Sync(opts)
	if err != nil || result.Source != "local" || result.Atomic {
		t.Fatalf("rollback accepted: %+v %v", result, err)
	}
	if err := skills.ValidateSignedRelease(recovered); err != nil {
		t.Fatal(err)
	}
	recovered.Revision = "0000000000000000"
	if err := skills.ValidateSignedRelease(recovered); err == nil {
		t.Fatal("tampering accepted")
	}
	if err := skills.SignManifest(recovered, key); err != nil {
		t.Fatal(err)
	}
	if err := skills.ValidateSignedRelease(recovered); err == nil {
		t.Fatal("signed inconsistent revision accepted")
	}
}

func TestConfiguredReleaseRequiresSupportedCLI(t *testing.T) {
	configBytes, err := os.ReadFile("../.cli.config")
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{}
	for _, line := range strings.Split(string(configBytes), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && !strings.HasPrefix(key, "#") {
			settings[key] = value
		}
	}
	if settings["CLI_VERSION"] == "" || settings["SKILLS_MIN_CLI_VERSION"] == "" {
		t.Fatal("release configuration must specify CLI and minimum compatible versions")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	previousKey := skills.VerifyPublicKeyBase64
	skills.VerifyPublicKeyBase64 = base64.StdEncoding.EncodeToString(publicKey)
	t.Cleanup(func() { skills.VerifyPublicKeyBase64 = previousKey })
	manifest, archive := signedRelease(t, privateKey, 10, "attribution-aware installation")
	manifest.CLIVersion = settings["CLI_VERSION"]
	manifest.MinCLIVersion = settings["SKILLS_MIN_CLI_VERSION"]
	if err := skills.SignManifest(manifest, privateKey); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var tarRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/latest/manifest.json", "/cli/latest/manifest.json":
			_, _ = w.Write(manifestBytes)
		case "/skills/latest/skills.tar.gz", "/cli/latest/skills.tar.gz":
			tarRequests.Add(1)
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	options := skills.SyncOptions{
		Into: filepath.Join(t.TempDir(), "skills"), CLIVersion: "0.0.42", CDNBase: server.URL,
	}
	// These published CLIs lack required attribution or direct runtime/JSON flags.
	for _, oldVersion := range []string{"0.0.42", "0.0.49", "0.0.50", "0.0.51"} {
		options.CLIVersion = oldVersion
		if _, err := skills.Sync(options); err == nil || !strings.Contains(err.Error(), "upgrade the CLI") {
			t.Fatalf("incompatible CLI %s was not rejected: %v", oldVersion, err)
		}
	}
	if tarRequests.Load() != 0 {
		t.Fatal("incompatible CLI downloaded the release archive")
	}
	options.CLIVersion = settings["CLI_VERSION"]
	result, err := skills.Sync(options)
	if err != nil || result == nil || !result.Atomic || !result.VerifiedManifest {
		t.Fatalf("configured CLI could not install its signed Skills release: result=%+v err=%v", result, err)
	}
	content, err := os.ReadFile(filepath.Join(options.Into, "ef-profile", "SKILL.md"))
	if err != nil || string(content) != "attribution-aware installation" {
		t.Fatalf("configured CLI did not install the expected release: content=%q err=%v", content, err)
	}
}
