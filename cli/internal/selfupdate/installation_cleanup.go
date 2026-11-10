package selfupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
)

// InstallationCleanup pins only files authenticated by retained signed packages.
// The caller holds installation, update and activation locks until removal ends.
type InstallationCleanup struct {
	Files       map[string]string                     `json:"files"`
	Directories []string                              `json:"directories"`
	Packages    map[string]InstallationCleanupPackage `json:"packages"`
}

// Retain signed metadata so an interrupted cleanup can authenticate the archive
// even after one of its on-disk manifest files has already been removed.
type InstallationCleanupPackage struct {
	Manifest      []byte `json:"manifest"`
	EntryManifest []byte `json:"entry_manifest"`
}

func PlanInstallationCleanup(ctx context.Context, root string, key ed25519.PublicKey) (*InstallationCleanup, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, fmt.Errorf("invalid installation cleanup root")
	}
	versions := root + ".versions"
	info, err := os.Lstat(versions)
	if os.IsNotExist(err) {
		if _, err := os.Lstat(root + ".current.json"); !os.IsNotExist(err) {
			return nil, fmt.Errorf("active installation pointer has no verified versions directory")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("installation versions must be a real directory")
	}
	entries, err := os.ReadDir(versions)
	if err != nil {
		return nil, err
	}
	plan := &InstallationCleanup{Files: map[string]string{}, Packages: map[string]InstallationCleanupPackage{}}
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !ValidVersion(entry.Name()) {
			return nil, fmt.Errorf("unverified installation version entry %q; preserve it", entry.Name())
		}
		dir := filepath.Join(versions, entry.Name())
		legacy, err := readInstallationFile(filepath.Join(dir, "release.json"), 1<<20)
		if err != nil {
			return nil, err
		}
		manifestData := legacy
		if data, err := readInstallationFile(filepath.Join(dir, ".package-release.json"), 1<<20); err == nil {
			manifestData = data
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		manifest, err := Verify(manifestData, key)
		if err != nil {
			return nil, err
		}
		if manifest.Version != entry.Name() {
			return nil, fmt.Errorf("installation cleanup version mismatch")
		}
		if manifest.NotifierVersion != "" {
			if err := validateLegacyEntryManifest(legacy, *manifest, key); err != nil {
				return nil, err
			}
		}
		artifact, ok := manifest.Packages[name+".zip"]
		if !ok {
			return nil, fmt.Errorf("signed installation package is missing")
		}
		binary, ok := manifest.Artifacts[name]
		if !ok {
			return nil, fmt.Errorf("signed installation executable is missing")
		}
		if err := validateInstalledPackage(ctx, dir, artifact, binary, versionExecutableName(), manifest.Version, "", manifestData, legacy); err != nil {
			return nil, fmt.Errorf("unverified installation package %s; preserve it: %w", entry.Name(), err)
		}
		plan.Packages[entry.Name()] = InstallationCleanupPackage{Manifest: manifestData, EntryManifest: legacy}
		if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				plan.Directories = append(plan.Directories, path)
				return nil
			}
			digest, err := packageFileHash(path)
			if err == nil {
				plan.Files[path] = digest
			}
			return err
		}); err != nil {
			return nil, err
		}
	}
	if _, err := os.Lstat(root + ".current.json"); err == nil {
		if _, err := ResolveInstallation(root, key); err != nil {
			return nil, err
		}
		digest, err := packageFileHash(root + ".current.json")
		if err != nil {
			return nil, err
		}
		plan.Files[root+".current.json"] = digest
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	plan.Directories = append(plan.Directories, versions)
	sortCleanupDirectories(plan.Directories)
	return plan, nil
}

func sortCleanupDirectories(directories []string) {
	sort.Slice(directories, func(i, j int) bool {
		if len(directories[i]) != len(directories[j]) {
			return len(directories[i]) > len(directories[j])
		}
		return directories[i] < directories[j]
	})
}

// RevalidateInstallationCleanup accepts missing owned files only for an existing
// receipt. Every file returned for deletion is authenticated again, rather than
// trusting paths or hashes supplied by the receipt. Callers retain all three
// installation locks across validation and deletion.
func RevalidateInstallationCleanup(ctx context.Context, root string, key ed25519.PublicKey, receipt *InstallationCleanup) (*InstallationCleanup, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, fmt.Errorf("invalid installation cleanup root")
	}
	if receipt == nil {
		live, err := PlanInstallationCleanup(ctx, root, key)
		if err != nil {
			return nil, err
		}
		if live != nil {
			return nil, fmt.Errorf("installation packages changed since uninstall began")
		}
		return nil, nil
	}
	versions := root + ".versions"
	if parent, err := filepath.EvalSymlinks(filepath.Dir(root)); err != nil || parent != filepath.Dir(root) {
		return nil, fmt.Errorf("installation cleanup root must not traverse links")
	}
	versionOf := func(file string, directory bool) (string, error) {
		if filepath.Clean(file) != file || !filepath.IsAbs(file) {
			return "", fmt.Errorf("invalid package cleanup path")
		}
		if (directory && file == versions) || (!directory && file == root+".current.json") {
			return "", nil
		}
		rel, err := filepath.Rel(versions, file)
		parts := strings.Split(rel, string(filepath.Separator))
		if err != nil || len(parts) == 0 || !ValidVersion(parts[0]) || (!directory && len(parts) < 2) {
			return "", fmt.Errorf("package cleanup path is outside a version directory")
		}
		if _, ok := receipt.Packages[parts[0]]; !ok {
			return "", fmt.Errorf("package cleanup path has no signed version")
		}
		return parts[0], nil
	}
	directories := map[string]bool{}
	for _, dir := range receipt.Directories {
		if _, err := versionOf(dir, true); err != nil {
			return nil, err
		}
		if directories[dir] {
			return nil, fmt.Errorf("duplicate package cleanup directory")
		}
		directories[dir] = true
	}
	for file := range receipt.Files {
		if _, err := versionOf(file, false); err != nil {
			return nil, err
		}
	}
	if !directories[versions] {
		return nil, fmt.Errorf("package cleanup omits versions directory")
	}
	live := &InstallationCleanup{Files: map[string]string{}, Packages: receipt.Packages}
	info, err := os.Lstat(versions)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("installation versions must be a real directory")
		}
		entries, err := os.ReadDir(versions)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if _, ok := receipt.Packages[entry.Name()]; !ok || !entry.IsDir() {
				return nil, fmt.Errorf("unverified installation version entry")
			}
		}
		live.Directories = append(live.Directories, versions)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	for version, proof := range receipt.Packages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !ValidVersion(version) {
			return nil, fmt.Errorf("invalid package cleanup version")
		}
		manifest, err := Verify(proof.Manifest, key)
		if err != nil {
			return nil, err
		}
		if manifest.Version != version {
			return nil, fmt.Errorf("package cleanup version mismatch")
		}
		if manifest.NotifierVersion != "" {
			if err := validateLegacyEntryManifest(proof.EntryManifest, *manifest, key); err != nil {
				return nil, err
			}
		} else if !bytes.Equal(proof.Manifest, proof.EntryManifest) {
			return nil, fmt.Errorf("unexpected cleanup entry manifest")
		}
		dir := filepath.Join(versions, version)
		if !directories[dir] {
			return nil, fmt.Errorf("package cleanup omits version directory")
		}
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			// No receipt path is used for deletion in an absent version.
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("installation version must be a real directory")
		}
		metadata := map[string]string{filepath.Join(dir, "release.json"): cleanupDigest(proof.EntryManifest)}
		if !bytes.Equal(proof.Manifest, proof.EntryManifest) {
			metadata[filepath.Join(dir, ".package-release.json")] = cleanupDigest(proof.Manifest)
		}
		name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		artifact, ok := manifest.Packages[name+".zip"]
		if !ok {
			return nil, fmt.Errorf("signed installation package is missing")
		}
		binary, ok := manifest.Artifacts[name]
		if !ok {
			return nil, fmt.Errorf("signed installation executable is missing")
		}
		metadata[filepath.Join(dir, packageArchive)] = artifact.SHA256
		for file, digest := range metadata {
			if receipt.Files[file] != digest {
				return nil, fmt.Errorf("cleanup metadata differs from signed package")
			}
		}
		archive, err := readInstallationFile(filepath.Join(dir, packageArchive), maxPackageBytes)
		var expected map[string]string
		expectedDirs := map[string]bool{dir: true}
		modes := map[string]os.FileMode{}
		if os.IsNotExist(err) {
			// The archive is removed only after every payload. Without it, never
			// delete payloads or nested directories based solely on the receipt.
			// Nested directories are removed before authentication metadata.
			expected = metadata
		} else {
			if err != nil {
				return nil, err
			}
			if err := packageDigest(archive, artifact); err != nil {
				return nil, err
			}
			zip, err := readPackage(bytes.NewReader(archive), int64(len(archive)))
			if err != nil {
				return nil, err
			}
			expected = map[string]string{}
			for file, digest := range metadata {
				expected[file] = digest
			}
			foundCLI, foundHelper := false, false
			for _, entry := range zip.File {
				rel := strings.TrimSuffix(entry.Name, "/")
				if rel != versionExecutableName() && !(runtime.GOOS == "darwin" && (rel == "EigenFlux Notifications.app" || strings.HasPrefix(rel, "EigenFlux Notifications.app/"))) {
					return nil, fmt.Errorf("unexpected cleanup package entry")
				}
				file := filepath.Join(dir, filepath.FromSlash(rel))
				for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
					expectedDirs[filepath.Join(dir, filepath.FromSlash(parent))] = true
				}
				if entry.FileInfo().IsDir() {
					expectedDirs[file] = true
					continue
				}
				in, err := entry.Open()
				if err != nil {
					return nil, err
				}
				hash := sha256.New()
				_, err = io.Copy(hash, in)
				closeErr := in.Close()
				if err == nil {
					err = closeErr
				}
				if err != nil {
					return nil, err
				}
				digest := hex.EncodeToString(hash.Sum(nil))
				expected[file] = digest
				modes[file] = entry.Mode()
				if rel == versionExecutableName() {
					foundCLI = true
					if digest != binary.SHA256 || int64(entry.UncompressedSize64) != binary.Size {
						return nil, fmt.Errorf("cleanup archive executable differs from signed binary")
					}
				}
				if rel == "EigenFlux Notifications.app/Contents/MacOS/EigenFluxNotifications" {
					foundHelper = true
				}
			}
			if !foundCLI || (runtime.GOOS == "darwin" && !foundHelper) {
				return nil, fmt.Errorf("cleanup archive omits required executable")
			}
			plannedFiles := map[string]string{}
			plannedDirs := map[string]bool{}
			for file, digest := range receipt.Files {
				if strings.HasPrefix(file, dir+string(filepath.Separator)) {
					plannedFiles[file] = digest
				}
			}
			for directory := range directories {
				if directory == dir || strings.HasPrefix(directory, dir+string(filepath.Separator)) {
					plannedDirs[directory] = true
				}
			}
			if !reflect.DeepEqual(plannedFiles, expected) || !reflect.DeepEqual(plannedDirs, expectedDirs) {
				return nil, fmt.Errorf("cleanup paths differ from signed archive")
			}
		}
		if err := filepath.WalkDir(dir, func(file string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("linked cleanup package entry")
			}
			if entry.IsDir() {
				if !expectedDirs[file] {
					return fmt.Errorf("unexpected cleanup package directory")
				}
				live.Directories = append(live.Directories, file)
				return nil
			}
			want, ok := expected[file]
			if !ok || !entry.Type().IsRegular() {
				return fmt.Errorf("unexpected cleanup package file")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if mode, ok := modes[file]; ok && runtime.GOOS != "windows" && info.Mode().Perm()&0111 != mode.Perm()&0111 {
				return fmt.Errorf("cleanup package executable mode differs")
			}
			have, err := packageFileHash(file)
			if err != nil {
				return err
			}
			if have != want {
				return fmt.Errorf("cleanup package file checksum differs")
			}
			live.Files[file] = want
			return nil
		}); err != nil {
			return nil, err
		}
	}
	pointer := root + ".current.json"
	if data, err := readInstallationFile(pointer, 4096); err == nil {
		if receipt.Files[pointer] == "" || cleanupDigest(data) != receipt.Files[pointer] {
			return nil, fmt.Errorf("installation pointer changed during cleanup")
		}
		var p installationPointer
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&p) != nil || !ValidVersion(p.Version) || decoder.Decode(new(any)) != io.EOF {
			return nil, fmt.Errorf("invalid cleanup installation pointer")
		}
		if _, ok := receipt.Packages[p.Version]; !ok {
			return nil, fmt.Errorf("cleanup pointer has no signed version")
		}
		live.Files[pointer] = receipt.Files[pointer]
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	sortCleanupDirectories(live.Directories)
	return live, nil
}

func cleanupDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Payloads precede their retained authentication material and the shared active
// pointer. In-use Windows executables fail before their metadata is removed.
func (p *InstallationCleanup) RemovalFiles(root string) []string {
	files := make([]string, 0, len(p.Files))
	for file := range p.Files {
		files = append(files, file)
	}
	rank := func(file string) int {
		if file == root+".current.json" {
			return 4
		}
		dir := filepath.Dir(file)
		if filepath.Dir(dir) == root+".versions" && ValidVersion(filepath.Base(dir)) {
			switch filepath.Base(file) {
			case versionExecutableName():
				return 0
			case "release.json", ".package-release.json":
				return 2
			case packageArchive:
				return 3
			}
		}
		return 1
	}
	sort.Slice(files, func(i, j int) bool {
		a, b := rank(files[i]), rank(files[j])
		if a != b {
			return a < b
		}
		return files[i] < files[j]
	})
	return files
}
