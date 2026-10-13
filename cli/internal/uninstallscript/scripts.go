// Package uninstallscript distributes the official wrappers with the CLI.
package uninstallscript

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed uninstall.sh
var POSIX []byte

//go:embed uninstall.ps1
var Windows []byte

func Asset() (string, []byte) {
	if runtime.GOOS == "windows" {
		return ".uninstall.ps1", Windows
	}
	return ".uninstall.sh", POSIX
}

func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Install only replaces a previously registered unchanged wrapper.
func Install(binary string, owned map[string]string) (string, string, error) {
	suffix, content := Asset()
	path := binary + suffix
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", "", errors.New("uninstall script must be a regular file")
		}
		old, err := os.ReadFile(path)
		if err != nil {
			return "", "", err
		}
		if Digest(old) != Digest(content) && (owned[path] == "" || owned[path] != Digest(old)) {
			return "", "", errors.New("preserve edited or unregistered uninstall script")
		}
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".uninstall-script-*")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0755); err == nil {
		_, err = f.Write(content)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", "", err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", "", err
	}
	return path, Digest(content), nil
}
