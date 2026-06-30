package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// isIgnoredBase reports whether a path component must be excluded from both the
// archive and the hash so build-side and client-side agree byte-for-byte.
func isIgnoredBase(base string) bool {
	return base == ".DS_Store" || strings.HasPrefix(base, "._")
}

// dirSHA256 computes a deterministic content hash of a skill directory.
//
// CRITICAL: this must produce an identical result on the build host
// (manifestgen) and on every client (verifyManifest re-hashes the extracted
// dir). A mismatch is the single most likely silent failure — it makes every
// sync fall back to "keep local", so the tool appears to run while never
// updating. To stay tar- and platform-agnostic: paths are slash-normalized and
// sorted; symlinks contribute their target string (never followed); empty dirs
// are entered as path records; .DS_Store/._* are ignored.
func dirSHA256(dir string) (string, error) {
	type entry struct {
		rel  string
		kind byte // 'F' file, 'D' dir, 'L' symlink
		link string
	}
	var entries []entry
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		base := filepath.Base(p)
		if isIgnoredBase(base) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			tgt, err := os.Readlink(p)
			if err != nil {
				return err
			}
			entries = append(entries, entry{rel: rel, kind: 'L', link: filepath.ToSlash(tgt)})
		case d.IsDir():
			entries = append(entries, entry{rel: rel + "/", kind: 'D'})
		default:
			entries = append(entries, entry{rel: rel, kind: 'F'})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	h := sha256.New()
	for _, e := range entries {
		io.WriteString(h, e.rel)
		h.Write([]byte{0})
		switch e.kind {
		case 'L':
			io.WriteString(h, "L")
			io.WriteString(h, e.link)
			h.Write([]byte{0})
		case 'D':
			io.WriteString(h, "D")
			h.Write([]byte{0})
		default:
			io.WriteString(h, "F")
			h.Write([]byte{0})
			f, err := os.Open(filepath.Join(dir, filepath.FromSlash(e.rel)))
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(h, f); err != nil {
				f.Close()
				return "", err
			}
			f.Close()
			h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// TarballSHA256 hashes a single file (used for the whole-tarball checksum
// recorded in the manifest and re-checked by clients before extraction).
func TarballSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// GenerateManifest walks srcDir for the allowlisted production skills and builds
// an authoritative manifest. A directory without SKILL.md is skipped; hidden
// directories are ignored. display_version is read best-effort from the
// SKILL.md frontmatter and never gates anything.
func GenerateManifest(srcDir, cliVersion, minCLIVersion string, allowlist []string, generatedAt int64) (*Manifest, error) {
	if len(allowlist) == 0 {
		allowlist = ProdAllowlist
	}
	m := &Manifest{
		CLIVersion:    cliVersion,
		MinCLIVersion: minCLIVersion,
		ManagedBy:     ManagedByValue,
		GeneratedAt:   generatedAt,
	}
	for _, name := range allowlist {
		sd := filepath.Join(srcDir, name)
		if !fileExists(filepath.Join(sd, "SKILL.md")) {
			return nil, fmt.Errorf("skill %q missing SKILL.md in %s", name, srcDir)
		}
		sum, err := dirSHA256(sd)
		if err != nil {
			return nil, fmt.Errorf("hash skill %q: %w", name, err)
		}
		m.Skills = append(m.Skills, SkillEntry{
			Name:           name,
			SHA256:         sum,
			DisplayVersion: readDisplayVersion(filepath.Join(sd, "SKILL.md")),
		})
	}
	sort.Slice(m.Skills, func(i, j int) bool { return m.Skills[i].Name < m.Skills[j].Name })
	m.Revision = computeRevision(m.Skills)
	return m, nil
}

// computeRevision is the content fingerprint over the sorted per-skill sha256s.
// It changes iff any skill's content changes — the freshness key, decoupled
// from the CLI version.
func computeRevision(skills []SkillEntry) string {
	ordered := append([]SkillEntry(nil), skills...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	h := sha256.New()
	for _, s := range ordered {
		io.WriteString(h, s.Name)
		h.Write([]byte{0})
		io.WriteString(h, s.SHA256)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16] // 64-bit prefix is plenty
}

// cliMeetsMin reports whether cliVersion >= minVersion (semver x.y.z compare).
// An empty floor or unparseable input is permissive (returns true).
func cliMeetsMin(cliVersion, minVersion string) bool {
	if strings.TrimSpace(minVersion) == "" {
		return true
	}
	return compareSemver(cliVersion, minVersion) >= 0
}

// compareSemver returns -1/0/1 for a<b / a==b / a>b over dotted numeric
// versions; non-numeric or missing components are treated as 0.
func compareSemver(a, b string) int {
	pa, pb := splitVer(a), splitVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func splitVer(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// drop any pre-release/build suffix
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.SplitN(v, ".", 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(strings.TrimSpace(parts[i]))
		out[i] = n
	}
	return out
}

// readDisplayVersion best-effort extracts `version:` from SKILL.md frontmatter
// metadata. Returns "" when absent — purely cosmetic.
func readDisplayVersion(skillMD string) string {
	data, err := os.ReadFile(skillMD)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		// matches `version: "0.6.0"` or `version: 0.6.0` under metadata
		if v, ok := strings.CutPrefix(t, "version:"); ok {
			v = strings.TrimSpace(v)
			v = strings.Trim(v, `"'`) // strip YAML quotes
			return v
		}
	}
	return ""
}

// ReadLocalManifest reads the manifest written into a skills dir. Returns
// (nil, nil) when absent (fresh install) — callers treat that as "no managed
// state yet", never as an error to surface.
func ReadLocalManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, nil // corrupt local manifest == treat as fresh, never block
	}
	return &m, nil
}

// WriteManifestAtomic writes the manifest into dir using the temp-file+rename
// pattern (mirrors auth/credentials.go), then fsyncs the directory so the new
// manifest is durable before the swap that exposes it.
func WriteManifestAtomic(dir string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".ef-manifest-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, manifestFilePerm); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, ManifestFileName)); err != nil {
		os.Remove(tmpPath)
		return err
	}
	fsyncDir(dir)
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
