package selfupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const InstallRootEnv = "EIGENFLUX_INSTALL_ROOT"
const InstallChildEnv = "EIGENFLUX_INSTALL_CHILD"
const installWatchLauncherEnv = "EIGENFLUX_INSTALL_WATCH_LAUNCHER"
const InstallRestartExitCode = 75

// InstallationRestart asks the stable Windows launcher to adopt the newly
// activated release after the caller has drained its admitted work.
type InstallationRestart struct{}

func (*InstallationRestart) Error() string { return "restart through stable installation entry" }

type Installation struct {
	Root, Version, Directory, Executable string
}

type installationPointer struct {
	Version string `json:"version"`
}

func versionExecutableName() string {
	if runtime.GOOS == "windows" {
		return "eigenflux.exe"
	}
	return "eigenflux"
}

// InstallationRoot accepts the inherited root only for the exact versioned
// layout. An arbitrary environment path never becomes an executable target.
func InstallationRoot(executable string) (string, error) {
	path, err := filepath.Abs(executable)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	root := os.Getenv(InstallRootEnv)
	if root == "" {
		return path, nil
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", fmt.Errorf("invalid stable installation root")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return "", fmt.Errorf("invalid stable installation root")
	}
	version := filepath.Base(filepath.Dir(path))
	if path == root {
		return root, nil
	}
	if !ValidVersion(version) || path != filepath.Join(root+".versions", version, versionExecutableName()) {
		return "", fmt.Errorf("executable is outside the stable installation")
	}
	return root, nil
}

// AdoptionInstallationRoot derives the stable root from the currently running
// process, never from the new release path. The first bare watch has not yet
// inherited InstallRootEnv when it adopts its first side-by-side release.
func AdoptionInstallationRoot(runningExecutable, nextExecutable string) (string, error) {
	root, err := InstallationRoot(runningExecutable)
	if err != nil {
		return "", err
	}
	next, err := filepath.Abs(nextExecutable)
	if err != nil {
		return "", err
	}
	next, err = filepath.EvalSymlinks(next)
	if err != nil {
		return "", err
	}
	if next == root {
		return root, nil
	}
	version := filepath.Base(filepath.Dir(next))
	if !ValidVersion(version) || next != filepath.Join(root+".versions", version, versionExecutableName()) {
		return "", fmt.Errorf("adoption executable is outside the stable installation")
	}
	return root, nil
}

// ResolveInstallation verifies the release signature and the executable hash
// on every launch. A pointer contains a version, never a caller-provided path.
func ResolveInstallation(root string, key ed25519.PublicKey) (*Installation, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, fmt.Errorf("invalid stable installation root")
	}
	data, err := readInstallationFile(root+".current.json", 4096)
	if err != nil {
		return nil, err
	}
	var pointer installationPointer
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pointer); err != nil || !ValidVersion(pointer.Version) {
		return nil, fmt.Errorf("invalid active installation pointer")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid active installation pointer")
	}
	return resolveInstallationVersion(root, pointer.Version, key)
}

func resolveInstallationVersion(root, version string, key ed25519.PublicKey) (*Installation, error) {
	if !ValidVersion(version) {
		return nil, fmt.Errorf("invalid installation version")
	}
	directory := filepath.Join(root+".versions", version)
	for _, path := range []string{root + ".versions", directory} {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("invalid installation directory")
		}
	}
	data, err := readInstallationFile(filepath.Join(directory, "release.json"), 1<<20)
	if err != nil {
		return nil, err
	}
	manifest, err := Verify(data, key)
	if err != nil {
		return nil, err
	}
	if manifest.Version != version {
		return nil, fmt.Errorf("installation release version mismatch")
	}
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	artifact, ok := manifest.Artifacts[name]
	if !ok || artifact.Size <= 0 || artifact.Size > 128<<20 {
		return nil, fmt.Errorf("installation executable artifact missing")
	}
	executable := filepath.Join(directory, versionExecutableName())
	bin, err := readInstallationFile(executable, artifact.Size)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(bin)
	if int64(len(bin)) != artifact.Size || hex.EncodeToString(sum[:]) != artifact.SHA256 {
		return nil, fmt.Errorf("installation executable checksum mismatch")
	}
	return &Installation{Root: root, Version: version, Directory: directory, Executable: executable}, nil
}

func readInstallationFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("invalid installation file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("installation file exceeds limit")
	}
	return data, err
}

// ActivateInstallation runs only after the package has been fully installed
// and probed. Releases are immutable; competing accounts share this pointer.
func ActivateInstallation(root, version string, key ed25519.PublicKey) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("invalid stable installation root")
	}
	lock, err := os.OpenFile(root+".activation.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	locked, err := tryLock(lock)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("installation activation is busy")
	}
	defer unlock(lock)
	if _, err := resolveInstallationVersion(root, version, key); err != nil {
		return err
	}
	_, pointerErr := os.Lstat(root + ".current.json")
	current, err := ResolveInstallation(root, key)
	if err == nil && Compare(current.Version, version) > 0 {
		return fmt.Errorf("installation downgrade rejected")
	}
	if err != nil && !errors.Is(pointerErr, os.ErrNotExist) {
		return fmt.Errorf("read active installation before activation: %w", err)
	}
	data, _ := json.Marshal(installationPointer{Version: version})
	tmp, err := os.CreateTemp(filepath.Dir(root), ".eigenflux-current-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := replace(tmp.Name(), root+".current.json"); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(root))
}

func InstallationEnvironment(env []string, root string) []string {
	out := make([]string, 0, len(env)+2)
	for _, value := range env {
		if !strings.HasPrefix(value, InstallRootEnv+"=") && !strings.HasPrefix(value, InstallChildEnv+"=") && !strings.HasPrefix(value, installWatchLauncherEnv+"=") {
			out = append(out, value)
		}
	}
	return append(out, InstallRootEnv+"="+root, InstallChildEnv+"=1")
}

func pinnedWatchEnvironment(env []string) bool {
	values := map[string]string{}
	for _, value := range env {
		if i := strings.IndexByte(value, '='); i >= 0 {
			values[value[:i]] = value[i+1:]
		}
	}
	return filepath.IsAbs(values["EIGENFLUX_HOME"]) && values["EIGENFLUX_WATCH_EXPECTED_AGENT"] != "" && values["EIGENFLUX_WATCH_EXPECTED_PRINCIPAL"] != ""
}

// ManagedInstallationChild is deliberately narrower than a general CLI
// subprocess: only a watch with pinned identity may use the restart exit code.
func ManagedInstallationChild() bool {
	if os.Getenv(installWatchLauncherEnv) != "1" || !pinnedWatchEnvironment(os.Environ()) {
		return false
	}
	path, err := os.Executable()
	if err != nil {
		return false
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	root, err := InstallationRoot(path)
	return err == nil && root != path
}
