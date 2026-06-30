package skills

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractTarGz extracts a gzip'd tar of skill folders into destDir, keeping only
// top-level entries whose first path component is in allow. destDir is created
// fresh. Defends against path traversal and absolute paths; ignores .DS_Store/._*.
func extractTarGz(data []byte, destDir string, allow []string) error {
	allowed := make(map[string]bool, len(allow))
	for _, a := range allow {
		allowed[a] = true
	}
	if err := os.MkdirAll(destDir, dirPerm); err != nil {
		return err
	}
	gz, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		name := filepath.ToSlash(strings.TrimPrefix(hdr.Name, "./"))
		if name == "" || name == "." {
			continue
		}
		// Reject traversal / absolute paths.
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		top := name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			top = name[:i]
		}
		if !allowed[top] {
			continue // skip anything outside the production allowlist
		}
		base := filepath.Base(name)
		if isIgnoredBase(base) {
			continue
		}
		target := filepath.Join(destDir, filepath.FromSlash(name))
		// Final containment check after Join.
		if rel, err := filepath.Rel(destDir, target); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("path escapes dest: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, dirPerm); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, skillFilePerm)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			// Production skills must not contain links (enforced at build time).
			// Refuse rather than silently materialize one.
			return fmt.Errorf("archive contains link entry %q (not allowed)", hdr.Name)
		default:
			// skip fifos/devices/etc.
		}
	}
	return nil
}

// copyDir recursively copies src to dst, skipping ignored files. Used to
// preserve unmanaged/third-party skill folders across a swap and for the
// bundle/EXDEV fallbacks.
func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, dirPerm); err != nil {
		return err
	}
	_ = info
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if isIgnoredBase(e.Name()) {
			continue
		}
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		switch {
		case e.IsDir():
			if err := copyDir(s, d); err != nil {
				return err
			}
		case e.Type()&os.ModeSymlink != 0:
			tgt, err := os.Readlink(s)
			if err != nil {
				return err
			}
			if err := os.Symlink(tgt, d); err != nil {
				return err
			}
		default:
			if err := copyFile(s, d); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, skillFilePerm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
