package selfupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type packageEntry struct {
	name, content string
	mode          os.FileMode
}

func testPackage(t *testing.T, entries ...packageEntry) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode == 0 {
			entry.mode = 0644
		}
		header.SetMode(entry.mode)
		out, err := w.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := out.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestExtractPackageRejectsUnsafeEntriesBeforeWriting(t *testing.T) {
	cases := map[string][]packageEntry{
		"parent": {{name: "../outside"}}, "absolute": {{name: "/outside"}},
		"backslash": {{name: `dir\outside`}}, "drive": {{name: "C:/outside"}},
		"link":   {{name: "link", content: "../outside", mode: os.ModeSymlink | 0777}},
		"device": {{name: "helper/CON.txt"}}, "alternate stream": {{name: "file:stream"}},
		"duplicate": {{name: "a"}, {name: "a"}}, "case duplicate": {{name: "A"}, {name: "a"}},
		"case parent": {{name: "A/x"}, {name: "a/y"}}, "file parent": {{name: "a"}, {name: "a/x"}},
		"file after child": {{name: "a/x"}, {name: "a"}}, "trailing dot": {{name: "a."}},
		"reserved archive": {{name: packageArchive}}, "nonportable": {{name: "Σ"}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			data := testPackage(t, entries...)
			dir := t.TempDir()
			if err := ExtractPackage(bytes.NewReader(data), int64(len(data)), dir); err == nil {
				t.Fatal("unsafe package accepted")
			}
			items, err := os.ReadDir(dir)
			if err != nil || len(items) != 0 {
				t.Fatalf("unsafe archive wrote files: %v %v", items, err)
			}
		})
	}
}

func TestExtractPackagePreservesExecutableAndRefusesNonEmptyDestination(t *testing.T) {
	data := testPackage(t, packageEntry{name: "eigenflux", content: "CLI", mode: 0755}, packageEntry{name: "Helper.app/Contents/Info.plist", content: "plist"})
	dir := t.TempDir()
	if err := ExtractPackage(bytes.NewReader(data), int64(len(data)), dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "eigenflux"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0755 {
		t.Fatalf("executable mode=%v", info.Mode())
	}
	if err := ExtractPackage(bytes.NewReader(data), int64(len(data)), dir); err == nil {
		t.Fatal("overwrote an installed directory")
	}
}

func TestExtractPackageRejectsEntryCountAndAdvertisedBomb(t *testing.T) {
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for i := 0; i <= maxPackageEntries; i++ {
		if _, err := w.Create(strings.Repeat("x", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ExtractPackage(bytes.NewReader(data.Bytes()), int64(data.Len()), t.TempDir()); err == nil {
		t.Fatal("entry limit ignored")
	}
	data.Reset()
	w = zip.NewWriter(&data)
	header := &zip.FileHeader{Name: "huge", Method: zip.Store, UncompressedSize64: maxPackageFileBytes + 1, CompressedSize64: 1}
	if _, err := w.CreateRaw(header); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ExtractPackage(bytes.NewReader(data.Bytes()), int64(data.Len()), t.TempDir()); err == nil {
		t.Fatal("advertised bomb accepted")
	}
}

func testArtifact(data []byte) Artifact {
	sum := sha256.Sum256(data)
	return Artifact{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

func TestStagePackageFailureRetainsOldInstallAndRejectsExistingVersion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "eigenflux")
	if err := os.WriteFile(root, []byte("old CLI"), 0755); err != nil {
		t.Fatal(err)
	}
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	data := testPackage(t, packageEntry{name: "eigenflux", content: "bad CLI", mode: 0755})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer server.Close()
	m := Manifest{Version: "9.8.7", Artifacts: map[string]Artifact{name: testArtifact([]byte("different binary"))}, Packages: map[string]Artifact{name + ".zip": testArtifact(data)}}
	if _, _, err := StagePackage(context.Background(), Options{CDN: server.URL, Home: t.TempDir(), Version: "1.0.0"}, m, root); err == nil {
		t.Fatal("mismatched package accepted")
	}
	got, _ := os.ReadFile(root)
	if string(got) != "old CLI" {
		t.Fatal("old installation changed")
	}
	destination := filepath.Join(root+".versions", m.Version)
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "sentinel"), []byte("in use"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := StagePackage(context.Background(), Options{CDN: server.URL, Home: t.TempDir(), Version: "1.0.0"}, m, root); err == nil {
		t.Fatal("existing version overwritten")
	}
	got, _ = os.ReadFile(filepath.Join(destination, "sentinel"))
	if string(got) != "in use" {
		t.Fatal("existing version modified")
	}
}

func TestStageCompletePackageProbesAndReusesExactFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a real version-probe fixture")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"9.8.7\")}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		fixture += ".exe"
	}
	build := exec.Command("go", "build", "-o", fixture, source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build probe fixture: %v %s", err, out)
	}
	cli, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	executable := "eigenflux"
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		executable += ".exe"
		name += ".exe"
	}
	entries := []packageEntry{{name: executable, content: string(cli), mode: 0755}}
	if runtime.GOOS == "darwin" {
		app := filepath.Join(dir, "EigenFlux Notifications.app")
		macos := filepath.Join(app, "Contents", "MacOS")
		if err := os.MkdirAll(macos, 0755); err != nil {
			t.Fatal(err)
		}
		helper := filepath.Join(macos, "EigenFluxNotifications")
		if err := os.WriteFile(helper, cli, 0755); err != nil {
			t.Fatal(err)
		}
		plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>ai.eigenflux.update-test</string><key>CFBundleExecutable</key><string>EigenFluxNotifications</string><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleVersion</key><string>9.8.7</string><key>CFBundleShortVersionString</key><string>9.8.7</string></dict></plist>`
		if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
			t.Fatalf("sign isolated helper: %v %s", err, out)
		}
		if err := filepath.WalkDir(app, func(file string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(dir, file)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			entries = append(entries, packageEntry{name: filepath.ToSlash(relative), content: string(data), mode: info.Mode()})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	data := testPackage(t, entries...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer server.Close()
	m := Manifest{Version: "9.8.7", Artifacts: map[string]Artifact{name: testArtifact(cli)}, Packages: map[string]Artifact{name + ".zip": testArtifact(data)}}
	root := filepath.Join(dir, "installed")
	if err := os.WriteFile(root, []byte("old installation"), 0755); err != nil {
		t.Fatal(err)
	}
	var phases []string
	options := Options{CDN: server.URL, Home: dir, Version: "1.0.0", OnProgress: func(p Progress) { phases = append(phases, p.Phase) }}
	staged, exe, err := StagePackage(context.Background(), options, m, root)
	if err != nil {
		t.Fatal(err)
	}
	if staged != filepath.Join(root+".versions", m.Version) || exe != filepath.Join(staged, executable) {
		t.Fatal("wrong immutable package paths")
	}
	if fmt.Sprint(phases) != "[download verify probe install]" {
		t.Fatalf("progress = %v", phases)
	}
	server.Close()
	if _, _, err := StagePackage(context.Background(), options, m, root); err != nil {
		t.Fatalf("verified package was not reused offline: %v", err)
	}
	if err := os.WriteFile(exe, []byte("tampered"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := StagePackage(context.Background(), options, m, root); err == nil {
		t.Fatal("modified version directory accepted")
	}
	old, _ := os.ReadFile(root)
	if string(old) != "old installation" {
		t.Fatal("staging replaced stable executable")
	}
}
