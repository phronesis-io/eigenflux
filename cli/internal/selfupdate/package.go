package selfupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	maxPackageBytes     = 256 << 20
	maxExpandedBytes    = 512 << 20
	maxPackageFileBytes = 128 << 20
	maxPackageEntries   = 4096
	packageArchive      = ".package.zip"
)

// StagePackage installs an immutable, signed CLI/helper pair. The caller must
// verify the manifest and hold the installation's update lock before calling.
// It never replaces a running executable or changes the active version pointer.
func StagePackage(ctx context.Context, o Options, m Manifest, rootExecutable string) (string, string, error) {
	if !ValidVersion(m.Version) || !filepath.IsAbs(rootExecutable) {
		return "", "", fmt.Errorf("invalid package version or installation path")
	}
	progress := func(phase string) {
		if o.OnProgress != nil {
			observe(func() { o.OnProgress(Progress{Phase: phase, TargetVersion: m.Version}) })
		}
	}
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	executable := "eigenflux"
	if runtime.GOOS == "windows" {
		name += ".exe"
		executable += ".exe"
	}
	artifact, ok := m.Packages[name+".zip"]
	if !ok || artifact.Size <= 0 || artifact.Size > maxPackageBytes {
		return "", "", fmt.Errorf("missing or invalid complete CLI package for %s", name)
	}
	binary, ok := m.Artifacts[name]
	if !ok || binary.Size <= 0 || binary.Size > maxPackageFileBytes {
		return "", "", fmt.Errorf("missing or invalid packaged CLI checksum")
	}
	versions := rootExecutable + ".versions"
	if err := packageDirectory(versions); err != nil {
		return "", "", atStep("package_directory", err)
	}
	manifestData, err := json.Marshal(m)
	if err != nil {
		return "", "", err
	}
	destination := filepath.Join(versions, m.Version)
	if _, err := os.Lstat(destination); err == nil {
		progress("verify")
		if err := validateInstalledPackage(ctx, destination, artifact, binary, executable, m.Version, o.Home, manifestData); err != nil {
			return "", "", fmt.Errorf("existing version directory is not the signed package: %w", err)
		}
		progress("probe")
		if err := probe(ctx, filepath.Join(destination, executable), m.Version, o.Home); err != nil {
			return "", "", err
		}
		return destination, filepath.Join(destination, executable), nil
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	progress("download")
	data, err := download(ctx, client, strings.TrimRight(o.CDN, "/")+"/cli/"+m.Version+"/"+name+".zip", artifact.Size, o.Version)
	if err != nil {
		return "", "", err
	}
	progress("verify")
	if err := packageDigest(data, artifact); err != nil {
		return "", "", err
	}
	stage, err := os.MkdirTemp(versions, ".stage-*")
	if err != nil {
		return "", "", atStep("package_stage", err)
	}
	defer os.RemoveAll(stage)
	if err := ExtractPackage(bytes.NewReader(data), int64(len(data)), stage); err != nil {
		return "", "", atStep("package_extract", err)
	}
	if err := writePackageArchive(filepath.Join(stage, packageArchive), data); err != nil {
		return "", "", atStep("package_archive", err)
	}
	if err := writePackageArchive(filepath.Join(stage, "release.json"), manifestData); err != nil {
		return "", "", atStep("package_manifest", err)
	}
	if err := validateInstalledPackage(ctx, stage, artifact, binary, executable, m.Version, o.Home, manifestData); err != nil {
		return "", "", err
	}
	progress("probe")
	if err := probe(ctx, filepath.Join(stage, executable), m.Version, o.Home); err != nil {
		return "", "", err
	}
	progress("install")
	if err := syncDirectory(stage); err != nil {
		return "", "", atStep("package_sync", err)
	}
	if err := os.Rename(stage, destination); err != nil {
		return "", "", atStep("package_rename", fmt.Errorf("install immutable CLI package: %w", err))
	}
	if err := syncDirectory(versions); err != nil {
		return "", "", atStep("versions_sync", err)
	}
	return destination, filepath.Join(destination, executable), nil
}

func packageDirectory(dir string) error {
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("package directory must be a real directory")
	}
	return nil
}

func packageDigest(data []byte, artifact Artifact) error {
	sum := sha256.Sum256(data)
	if int64(len(data)) != artifact.Size || hex.EncodeToString(sum[:]) != artifact.SHA256 {
		return fmt.Errorf("CLI package checksum or size mismatch")
	}
	return nil
}

func writePackageArchive(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

// ExtractPackage accepts only bounded, regular files with portable relative
// names. The destination must be an empty real directory owned by the caller.
func ExtractPackage(reader io.ReaderAt, size int64, destination string) error {
	info, err := os.Lstat(destination)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid package destination")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("package destination is not empty")
	}
	r, err := readPackage(reader, size)
	if err != nil {
		return err
	}
	for _, entry := range r.File {
		name := filepath.Join(destination, filepath.FromSlash(strings.TrimSuffix(entry.Name, "/")))
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(name, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if entry.Mode().Perm()&0111 != 0 {
			mode = 0755
		}
		out, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		in, openErr := entry.Open()
		if openErr == nil {
			var copied int64
			copied, openErr = io.Copy(out, io.LimitReader(in, int64(entry.UncompressedSize64)+1))
			if openErr == nil && uint64(copied) != entry.UncompressedSize64 {
				openErr = fmt.Errorf("package entry size mismatch")
			}
			if closeErr := in.Close(); openErr == nil {
				openErr = closeErr
			}
		}
		if openErr == nil {
			openErr = out.Sync()
		}
		if closeErr := out.Close(); openErr == nil {
			openErr = closeErr
		}
		if openErr != nil {
			return openErr
		}
	}
	return filepath.WalkDir(destination, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return syncDirectory(name)
		}
		return nil
	})
}

func readPackage(reader io.ReaderAt, size int64) (*zip.Reader, error) {
	if size <= 0 || size > maxPackageBytes {
		return nil, fmt.Errorf("invalid CLI package size")
	}
	r, err := zip.NewReader(reader, size)
	if err != nil {
		return nil, err
	}
	if len(r.File) == 0 || len(r.File) > maxPackageEntries {
		return nil, fmt.Errorf("invalid CLI package entry count")
	}
	seen := map[string]bool{}
	spelling := map[string]string{}
	files := map[string]bool{}
	var expanded uint64
	for _, entry := range r.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if name == "" || name == "." || name == packageArchive || name == "release.json" || path.Clean(name) != name || path.IsAbs(name) || strings.ContainsAny(name, "\\:\x00") {
			return nil, fmt.Errorf("unsafe CLI package path")
		}
		for _, part := range strings.Split(name, "/") {
			for _, character := range part {
				if character < 32 || character > 126 {
					return nil, fmt.Errorf("non-portable CLI package path")
				}
			}
			if part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.ContainsAny(part, "<>\"|?*") {
				return nil, fmt.Errorf("unsafe CLI package path component")
			}
			device := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
			if device == "CON" || device == "PRN" || device == "AUX" || device == "NUL" || (len(device) == 4 && (strings.HasPrefix(device, "COM") || strings.HasPrefix(device, "LPT")) && device[3] >= '0' && device[3] <= '9') {
				return nil, fmt.Errorf("device name in CLI package")
			}
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, fmt.Errorf("duplicate CLI package path")
		}
		seen[key] = true
		mode := entry.Mode()
		if !mode.IsRegular() && !mode.IsDir() {
			return nil, fmt.Errorf("non-regular CLI package entry")
		}
		for current := name; current != "."; current = path.Dir(current) {
			key := strings.ToLower(current)
			if previous, ok := spelling[key]; ok && previous != current {
				return nil, fmt.Errorf("case-colliding CLI package path")
			}
			spelling[key] = current
			if current != name && files[key] {
				return nil, fmt.Errorf("CLI package file used as directory")
			}
		}
		if !mode.IsDir() {
			for previous := range spelling {
				if strings.HasPrefix(previous, key+"/") {
					return nil, fmt.Errorf("CLI package directory used as file")
				}
			}
			files[key] = true
		}
		if mode.IsDir() && entry.UncompressedSize64 != 0 {
			return nil, fmt.Errorf("invalid CLI package directory")
		}
		if entry.UncompressedSize64 > maxPackageFileBytes {
			return nil, fmt.Errorf("CLI package file exceeds limit")
		}
		expanded += entry.UncompressedSize64
		if expanded > maxExpandedBytes {
			return nil, fmt.Errorf("expanded CLI package exceeds limit")
		}
	}
	return r, nil
}

func validateInstalledPackage(ctx context.Context, dir string, artifact, binary Artifact, executable, version, home string, manifestData []byte) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid installed package directory")
	}
	archiveInfo, err := os.Lstat(filepath.Join(dir, packageArchive))
	if err != nil {
		return err
	}
	if !archiveInfo.Mode().IsRegular() || archiveInfo.Size() != artifact.Size || archiveInfo.Size() > maxPackageBytes {
		return fmt.Errorf("invalid retained package archive")
	}
	data, err := os.ReadFile(filepath.Join(dir, packageArchive))
	if err != nil {
		return err
	}
	if err := packageDigest(data, artifact); err != nil {
		return err
	}
	r, err := readPackage(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	manifestInfo, err := os.Lstat(filepath.Join(dir, "release.json"))
	if err != nil {
		return err
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Size() > 1<<20 {
		return fmt.Errorf("invalid installed package release manifest")
	}
	savedManifest, err := os.ReadFile(filepath.Join(dir, "release.json"))
	if err != nil || !bytes.Equal(savedManifest, manifestData) {
		return fmt.Errorf("installed package release manifest differs")
	}
	expected := map[string]bool{packageArchive: true, "release.json": true}
	helper := false
	for _, entry := range r.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if name != executable && !(runtime.GOOS == "darwin" && (name == "EigenFlux Notifications.app" || strings.HasPrefix(name, "EigenFlux Notifications.app/"))) {
			return fmt.Errorf("unexpected file in CLI package")
		}
		expected[name] = true
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			expected[parent] = true
		}
		file := filepath.Join(dir, filepath.FromSlash(name))
		info, err := os.Lstat(file)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != entry.FileInfo().IsDir() {
			return fmt.Errorf("installed package entry type mismatch")
		}
		if info.IsDir() {
			continue
		}
		if !info.Mode().IsRegular() || uint64(info.Size()) != entry.UncompressedSize64 {
			return fmt.Errorf("installed package file differs")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0111 != entry.Mode().Perm()&0111 {
			return fmt.Errorf("installed package executable mode differs")
		}
		in, err := entry.Open()
		if err != nil {
			return err
		}
		archiveHash := sha256.New()
		_, err = io.Copy(archiveHash, in)
		if closeErr := in.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		fileHash, err := packageFileHash(file)
		if err != nil || fileHash != hex.EncodeToString(archiveHash.Sum(nil)) {
			return fmt.Errorf("installed package file checksum differs")
		}
		if name == "EigenFlux Notifications.app/Contents/MacOS/EigenFluxNotifications" {
			helper = true
		}
	}
	if err := filepath.WalkDir(dir, func(file string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if file == dir {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("non-regular installed package entry")
		}
		rel, err := filepath.Rel(dir, file)
		if err != nil {
			return err
		}
		if !expected[filepath.ToSlash(rel)] {
			return fmt.Errorf("unexpected installed package entry")
		}
		return nil
	}); err != nil {
		return err
	}
	exe := filepath.Join(dir, executable)
	cliInfo, err := os.Lstat(exe)
	if err != nil {
		return err
	}
	cliHash, err := packageFileHash(exe)
	if err != nil || cliInfo.Size() != binary.Size || cliHash != binary.SHA256 {
		return fmt.Errorf("packaged CLI checksum differs from signed binary")
	}
	if runtime.GOOS == "darwin" {
		if !helper {
			return fmt.Errorf("complete macOS package requires notification helper")
		}
		verifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		for _, key := range []string{"CFBundleVersion", "CFBundleShortVersionString"} {
			value, err := exec.CommandContext(verifyCtx, "/usr/libexec/PlistBuddy", "-c", "Print :"+key, filepath.Join(dir, "EigenFlux Notifications.app", "Contents", "Info.plist")).Output()
			if err != nil || strings.TrimSpace(string(value)) != version {
				return fmt.Errorf("notification helper version differs from CLI release")
			}
		}
		if err := exec.CommandContext(verifyCtx, "/usr/bin/codesign", "--verify", "--deep", "--strict", filepath.Join(dir, "EigenFlux Notifications.app")).Run(); err != nil {
			return fmt.Errorf("verify packaged notification helper: %w", err)
		}
	}
	return nil
}

func packageFileHash(name string) (string, error) {
	in, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer in.Close()
	hash := sha256.New()
	_, err = io.Copy(hash, in)
	return hex.EncodeToString(hash.Sum(nil)), err
}
