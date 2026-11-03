package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/selfupdate"
)

func TestVerifyPublished(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"same", "newer", "old", "bad-signature", "missing-entry", "missing-artifact", "corrupt", "short", "server-error", "manifest-error", "invalid-minimum", "network-error"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte("binary")
			sum := sha256.Sum256(body)
			m := selfupdate.Manifest{Version: "0.0.48", Artifacts: map[string]selfupdate.Artifact{}}
			if mode == "old" {
				m.Version = "0.0.47"
			}
			if mode == "newer" {
				m.Version = "0.0.49"
			}
			for _, p := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64.exe", "windows-arm64.exe"} {
				m.Artifacts["eigenflux-"+p] = selfupdate.Artifact{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
			}
			if mode == "missing-entry" {
				delete(m.Artifacts, "eigenflux-windows-arm64.exe")
			}
			if err := selfupdate.Sign(&m, key); err != nil {
				t.Fatal(err)
			}
			if mode == "bad-signature" {
				m.Version = "0.0.50"
			}
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/cli/v2/latest/release.json" || r.URL.Path == "/cli/v2/latest/package-release.json" {
					http.NotFound(w, r)
					return
				}

				if r.URL.Path == "/cli/latest/package-release.json" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.URL.Path == "/cli/latest/release.json" {
					if mode == "manifest-error" {
						w.WriteHeader(503)
						return
					}
					w.Write(data)
					return
				}
				if !strings.HasPrefix(r.URL.Path, "/cli/"+m.Version+"/") {
					t.Errorf("unversioned artifact URL: %s", r.URL.Path)
				}
				switch mode {
				case "missing-artifact":
					w.WriteHeader(404)
				case "server-error":
					w.WriteHeader(503)
				case "corrupt":
					w.Write([]byte("broken"))
				case "short":
					w.Write(body[:2])
				default:
					w.Write(body)
				}
			}))
			defer server.Close()
			if mode == "network-error" {
				server.Close()
			}
			minimum := "0.0.48"
			if mode == "invalid-minimum" {
				minimum = ""
			}
			err = verifyPublished(server.URL, minimum, pub)
			wantOK := mode == "same" || mode == "newer"
			if (err == nil) != wantOK {
				t.Fatalf("verifyPublished = %v, want success %v", err, wantOK)
			}
		})
	}
}

func TestVerifyPublishedCompletePackages(t *testing.T) {
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "missing", "corrupt", "unsigned-extension"} {
		t.Run(mode, func(t *testing.T) {
			binary, archive := []byte("bare CLI"), []byte("complete package")
			checksum := func(body []byte) selfupdate.Artifact {
				hash := sha256.Sum256(body)
				return selfupdate.Artifact{SHA256: hex.EncodeToString(hash[:]), Size: int64(len(body))}
			}
			manifest := selfupdate.Manifest{Version: "1.0.9", Artifacts: map[string]selfupdate.Artifact{}, Packages: map[string]selfupdate.Artifact{}}
			for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64.exe", "windows-arm64.exe"} {
				manifest.Artifacts["eigenflux-"+platform] = checksum(binary)
				manifest.Packages["eigenflux-"+platform+".zip"] = checksum(archive)
			}
			if mode == "missing" {
				delete(manifest.Packages, "eigenflux-darwin-arm64.zip")
			}
			if err := selfupdate.Sign(&manifest, key); err != nil {
				t.Fatal(err)
			}
			if mode == "unsigned-extension" {
				manifest.Packages["eigenflux-darwin-arm64.zip"] = checksum([]byte("untrusted"))
			}
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			legacy := manifest
			legacy.Packages = nil
			if err := selfupdate.Sign(&legacy, key); err != nil {
				t.Fatal(err)
			}
			legacyData, err := json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			packagesRead := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/cli/v2/latest/release.json" || r.URL.Path == "/cli/v2/latest/package-release.json" {
					http.NotFound(w, r)
					return
				}

				if strings.HasSuffix(r.URL.Path, "package-release.json") {
					_, _ = w.Write(data)
					return
				}
				if strings.HasSuffix(r.URL.Path, "release.json") {
					_, _ = w.Write(legacyData)
					return
				}
				if strings.HasSuffix(r.URL.Path, ".zip") {
					packagesRead++
					if mode == "corrupt" {
						_, _ = w.Write([]byte("bad package"))
						return
					}
					_, _ = w.Write(archive)
					return
				}
				_, _ = w.Write(binary)
			}))
			defer server.Close()
			err = verifyPublished(server.URL, "1.0.0", public)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("verification mode %s: %v", mode, err)
			}
			if mode == "valid" && packagesRead != 6 {
				t.Fatalf("verified %d packages, want 6", packagesRead)
			}
		})
	}
}

func TestVerifyPublishedV2IndependentFromLegacyLane(t *testing.T) {
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	binary, archive := []byte("CLI"), []byte("independent notification package")
	checksum := func(b []byte) selfupdate.Artifact {
		sum := sha256.Sum256(b)
		return selfupdate.Artifact{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}
	}
	m := selfupdate.Manifest{Version: "1.0.6", NotifierVersion: "1.0.5", Artifacts: map[string]selfupdate.Artifact{}, Packages: map[string]selfupdate.Artifact{}}
	for _, p := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64.exe", "windows-arm64.exe"} {
		m.Artifacts["eigenflux-"+p] = checksum(binary)
		m.Packages["eigenflux-"+p+".zip"] = checksum(archive)
	}
	_ = selfupdate.Sign(&m, key)
	packages, _ := json.Marshal(m)
	m.Packages = nil
	_ = selfupdate.Sign(&m, key)
	bare, _ := json.Marshal(m)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cli/v2/latest/release.json":
			_, _ = w.Write(bare)
		case "/cli/v2/latest/package-release.json":
			_, _ = w.Write(packages)
		default:
			if !strings.HasPrefix(r.URL.Path, "/cli/1.0.6/") {
				t.Errorf("v2 verifier used unrelated legacy lane: %s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			if strings.HasSuffix(r.URL.Path, ".zip") {
				_, _ = w.Write(archive)
			} else {
				_, _ = w.Write(binary)
			}
		}
	}))
	defer server.Close()
	if err := verifyPublished(server.URL, "1.0.6", public); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyPublishedV2FailureNeverFallsBack(t *testing.T) {
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	for _, mode := range []string{"http_error", "invalid_signature", "legacy_payload"} {
		t.Run(mode, func(t *testing.T) {
			m := selfupdate.Manifest{Version: "1.0.6", NotifierVersion: "1.0.5"}
			if mode == "legacy_payload" {
				m.NotifierVersion = ""
			}
			_ = selfupdate.Sign(&m, key)
			if mode == "invalid_signature" {
				m.Version = "1.0.7"
			}
			legacyRequests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/cli/v2/latest/release.json" {
					legacyRequests++
					t.Errorf("unsafe fallback %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if mode == "http_error" {
					w.WriteHeader(503)
					return
				}
				_ = json.NewEncoder(w).Encode(m)
			}))
			defer server.Close()
			if err := verifyPublished(server.URL, "1.0.5", public); err == nil || legacyRequests != 0 {
				t.Fatalf("untrusted v2 accepted/fell back: %v %d", err, legacyRequests)
			}
		})
	}
}

func TestPreparedManifestSignatures(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	for _, name := range []string{"release.json", "package-release.json", "release-v2.json", "package-release-v2.json"} {
		m := selfupdate.Manifest{Version: "1.0.6", Artifacts: map[string]selfupdate.Artifact{}}
		if strings.Contains(name, "-v2.") {
			m.NotifierVersion = "1.0.5"
		}
		if err := selfupdate.Sign(&m, key); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyLocalSignatures(dir, "1.0.6", pub); err != nil {
		t.Fatal(err)
	}
	if err := verifyLocalSignatures(dir, "1.0.7", pub); err == nil {
		t.Fatal("wrong version accepted")
	}
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	if err := verifyLocalSignatures(dir, "1.0.6", wrong.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("wrong trust root accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "release.json"), []byte(`{"version":"1.0.6","artifacts":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyLocalSignatures(dir, "1.0.6", pub); err == nil {
		t.Fatal("unsigned transferred manifest accepted")
	}
}
