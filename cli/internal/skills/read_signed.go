package skills

import (
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ReadSignedFile reads one file belonging to the installed signed release.
// It never synchronizes, repairs the installation, or acquires a writer lock.
func ReadSignedFile(dir, skillName, relativePath string, maxBytes int64) ([]byte, error) {
	if strings.TrimSpace(dir) == "" || !productionSkillName.MatchString(skillName) ||
		!fs.ValidPath(relativePath) || relativePath == "." || strings.ContainsAny(relativePath, "\\:\x00") ||
		maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return nil, fmt.Errorf("invalid signed Skill file request")
	}
	for _, part := range strings.Split(relativePath, "/") {
		if isIgnoredBase(part) {
			return nil, fmt.Errorf("Skill file is excluded from release hashes")
		}
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return nil, err
	}
	if err := signedReadSyncIdle(real); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(real)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if _, err := signedRegularFile(root, ManifestFileName); err != nil {
		return nil, err
	}
	path := filepath.Join(skillName, filepath.FromSlash(relativePath))
	info, err := signedRegularFile(root, path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("signed Skill file exceeds size limit")
	}
	before, err := readSyncSnapshot(real)
	if err != nil {
		return nil, err
	}
	if before.manifest == nil || before.manifest.ManagedBy != ManagedByValue || before.stale || !before.intact {
		return nil, fmt.Errorf("signed Skill installation is unavailable or modified")
	}
	if err := ValidateSignedRelease(before.manifest); err != nil {
		return nil, err
	}
	if _, present := before.manifest.names()[skillName]; !present {
		return nil, fmt.Errorf("Skill %q is not a signed release member", skillName)
	}
	rootInfo, err := root.Stat(".")
	if err != nil || !sameSignedReadFile(before.dir, rootInfo) {
		return nil, fmt.Errorf("Skill installation changed before reading")
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameSignedReadFile(info, opened) {
		return nil, fmt.Errorf("Skill file changed before reading")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes || !utf8.Valid(data) {
		return nil, fmt.Errorf("signed Skill file exceeds size limit or is not UTF-8")
	}
	after, err := readSyncSnapshot(real)
	if err != nil {
		return nil, err
	}
	current, err := signedRegularFile(root, path)
	if err != nil || !sameSignedReadFile(info, current) || !before.matches(after) || !after.intact {
		return nil, fmt.Errorf("Skill installation changed while reading")
	}
	if err := signedReadSyncIdle(real); err != nil {
		return nil, err
	}
	return data, nil
}

func signedReadSyncIdle(real string) error {
	if _, err := os.Lstat(filepath.Join(filepath.Dir(real), lockFileName)); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("Skill synchronization is in progress")
	}
	return nil
}

func signedRegularFile(root *os.Root, path string) (os.FileInfo, error) {
	var info os.FileInfo
	parts := strings.Split(filepath.ToSlash(path), "/")
	for index := range parts {
		var err error
		info, err = root.Lstat(filepath.Join(parts[:index+1]...))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (index < len(parts)-1 && !info.IsDir()) || (index == len(parts)-1 && !info.Mode().IsRegular()) {
			return nil, fmt.Errorf("signed Skill path must contain regular directories and a regular file, without symlinks")
		}
	}
	return info, nil
}

func sameSignedReadFile(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) &&
		before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) && before.Mode() == after.Mode()
}
