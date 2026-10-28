package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
