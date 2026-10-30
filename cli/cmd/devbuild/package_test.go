package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/selfupdate"
)

func TestPackagedCLIIncludesOnlySelectedPairAndBothSignedManifests(t *testing.T) {
	for _, target := range []string{"darwin/arm64", "windows/amd64", "linux/amd64"} {
		t.Run(target, func(t *testing.T) {
			out := t.TempDir()
			public, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			executable := "eigenflux"
			if strings.HasPrefix(target, "windows/") {
				executable += ".exe"
			}
			for name, content := range map[string]string{executable: "selected CLI", "test-source": "test server", "Run-Test.ps1": "wrapper", "private-test-key.pem": "excluded sentinel", "cdn/skills/latest/manifest.json": "skills"} {
				if err := writeFile(filepath.Join(out, filepath.FromSlash(name)), []byte(content)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(filepath.Join(out, executable), 0755); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(target, "darwin/") {
				for name, content := range map[string]string{"Contents/Info.plist": "<plist><dict><key>CFBundleVersion</key><string>9.8.7</string></dict></plist>", "Contents/MacOS/EigenFluxNotifications": "paired helper", "Contents/Resources/EigenFlux.icns": "paired icon"} {
					path := filepath.Join(out, "EigenFlux Notifications.app", filepath.FromSlash(name))
					if err := writeFile(path, []byte(content)); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(name, "MacOS/") {
						if err := os.Chmod(path, 0755); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if err := packageCLI(out, target, "9.8.7", key); err != nil {
				t.Fatal(err)
			}
			assertTestCLIPackage(t, out, target, "9.8.7", public)
		})
	}
}

func TestPackagedCLIRequiresCompleteMacHelper(t *testing.T) {
	out := t.TempDir()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(out, "eigenflux"), []byte("CLI")); err != nil {
		t.Fatal(err)
	}
	if err := packageCLI(out, "darwin/arm64", "9.8.7", key); err == nil {
		t.Fatal("published a package without its macOS helper")
	}
	if _, err := os.Stat(filepath.Join(out, "cdn", "cli", "latest", "package-release.json")); !os.IsNotExist(err) {
		t.Fatal("incomplete package advertised a manifest")
	}
}

func assertTestCLIPackage(t *testing.T, out, target, version string, public ed25519.PublicKey) {
	t.Helper()
	parts := strings.Split(target, "/")
	executable, name := "eigenflux", "eigenflux-"+parts[0]+"-"+parts[1]
	if parts[0] == "windows" {
		executable += ".exe"
		name += ".exe"
	}
	var complete *selfupdate.Manifest
	for _, filename := range []string{"release.json", "package-release.json"} {
		data, err := os.ReadFile(filepath.Join(out, "cdn", "cli", "latest", filename))
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := selfupdate.Verify(data, public)
		if err != nil {
			t.Fatalf("%s trust root mismatch: %v", filename, err)
		}
		if manifest.Version != version || len(manifest.Artifacts) != 1 {
			t.Fatalf("invalid %s: %+v", filename, manifest)
		}
		versioned, err := os.ReadFile(filepath.Join(out, "cdn", "cli", version, filename))
		if err != nil || !bytes.Equal(data, versioned) {
			t.Fatalf("versioned %s differs: %v", filename, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if filename == "release.json" {
			if _, exists := fields["packages"]; exists {
				t.Fatal("legacy signature payload gained packages")
			}
		} else {
			if len(manifest.Packages) != 1 {
				t.Fatalf("complete manifest packages=%v", manifest.Packages)
			}
			complete = manifest
		}
	}
	archive, err := os.ReadFile(filepath.Join(out, "cdn", "cli", version, name+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	artifact := complete.Packages[name+".zip"]
	sum := sha256.Sum256(archive)
	if artifact.Size != int64(len(archive)) || artifact.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("complete archive differs from signed manifest")
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	extracted := t.TempDir()
	if err := selfupdate.ExtractPackage(bytes.NewReader(archive), int64(len(archive)), extracted); err != nil {
		t.Fatalf("updater rejects its test package: %v", err)
	}
	helperFound, cliFound := false, false
	for _, file := range reader.File {
		if file.Name == executable {
			cliFound = true
		} else if parts[0] != "darwin" || !strings.HasPrefix(file.Name, "EigenFlux Notifications.app/") {
			t.Fatalf("unrelated test/private file entered update package: %s", file.Name)
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		source, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(file.Name)))
		if err != nil || !bytes.Equal(content, source) {
			t.Fatalf("paired package bytes differ: %s %v", file.Name, err)
		}
		if file.Name == "EigenFlux Notifications.app/Contents/MacOS/EigenFluxNotifications" {
			helperFound = true
		}
	}
	if !cliFound || ((parts[0] == "darwin") != helperFound) {
		t.Fatal("incomplete selected executable/helper pair")
	}
	binary, err := os.ReadFile(filepath.Join(extracted, executable))
	if err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256(binary)
	if complete.Artifacts[name].SHA256 != hex.EncodeToString(sum[:]) || complete.Artifacts[name].Size != int64(len(binary)) {
		t.Fatal("packaged CLI differs from separately signed bare artifact")
	}
}
