package skills

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Only these in-target entries belong to the installer. Staging, recovery and
// lock paths live beside the target and do not participate in preservation.
func isSkillsMetadata(name string) bool {
	return name == ManifestFileName || name == StaleMarkerName ||
		strings.HasPrefix(name, ".ef-manifest-") && strings.HasSuffix(name, ".tmp")
}

// Host content is not a release archive: retain hidden entries, ordinary mode
// bits and symlink text without applying the official archive's ignored files.
// Destinations are new staging entries; a collision or unsupported file type
// aborts installation before the active directory is swapped.
func copyHostEntry(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.Mkdir(dst, dirPerm); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyHostEntry(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
			return err
		}
		fsyncDir(dst)
		return nil
	case info.Mode().IsRegular():
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, manifestFilePerm)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Chmod(info.Mode().Perm()); err != nil {
			out.Close()
			return err
		}
		if err := out.Sync(); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	default:
		return fmt.Errorf("unsupported host metadata file type: %s", src)
	}
}
