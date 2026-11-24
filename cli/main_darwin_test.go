package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSandboxCertificateBundle(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		// Stop after TLS so this test cannot install a release.
		http.Error(w, "test manifest unavailable", http.StatusNotFound)
	}))
	defer server.Close()
	dir := t.TempDir()
	binary := filepath.Join(dir, "eigenflux")
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	flags := "-X main.Version=1.0.50 -X cli.eigenflux.ai/internal/skills.VerifyPublicKeyBase64=" + base64.StdEncoding.EncodeToString(pub)
	if out, err := exec.Command("go", "build", "-ldflags", flags, "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	bundle := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Run("offline help with missing bundle", func(t *testing.T) {
		c := exec.Command(binary, "--help")
		c.Env = append(os.Environ(), "HOME="+t.TempDir(), "EIGENFLUX_HOME="+t.TempDir(), "SSL_CERT_FILE="+filepath.Join(dir, "missing.pem"))
		if out, err := c.CombinedOutput(); err != nil || !strings.Contains(string(out), "Usage:") {
			t.Fatalf("help: %v: %s", err, out)
		}
	})
	profile := `(version 1)(allow default)(deny mach-lookup (global-name "com.apple.trustd") (global-name "com.apple.trustd.agent"))`
	for _, tc := range []struct {
		name, path, wantError string
		reachesServer         bool
		args                  []string
	}{
		{"default", "", "OSStatus -26276", false, []string{"update"}},
		{"trusted", bundle, "", true, []string{"update"}},
		{"skills", bundle, "", true, []string{"skills", "sync", "--into", filepath.Join(dir, "skills")}},
		{"untrusted", "/etc/ssl/cert.pem", "unknown authority", false, []string{"update"}},
		{"missing", filepath.Join(dir, "missing.pem"), "read SSL_CERT_FILE", false, []string{"update"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := requests.Load()
			c := exec.Command("/usr/bin/sandbox-exec", append([]string{"-p", profile, binary}, tc.args...)...)
			home := t.TempDir()
			c.Env = append(os.Environ(), "HOME="+home, "EIGENFLUX_HOME="+home, "EIGENFLUX_CDN_URL="+server.URL, "SSL_CERT_FILE="+tc.path)
			out, err := c.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.wantError) || (requests.Load() > before) != tc.reachesServer {
				t.Fatalf("requests=%d err=%v output=%s", requests.Load()-before, err, out)
			}
		})
	}
}
