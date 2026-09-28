package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const signedReadRule = "Read the frozen contract before returning a decision.\n"

func signedReadFixture(t *testing.T) string {
	t.Helper()
	dir := stageSkills(t, map[string]map[string]string{
		"ef-commission": {"SKILL.md": "Commission", "references/dispatch.md": signedReadRule},
		"ef-profile":    {"SKILL.md": "Profile"},
	})
	writeSignedReadManifest(t, dir, []string{"ef-commission", "ef-profile"})
	return dir
}

func writeSignedReadManifest(t *testing.T, dir string, names []string) {
	t.Helper()
	oldKey := VerifyPublicKeyBase64
	t.Cleanup(func() { VerifyPublicKeyBase64 = oldKey })
	manifest, err := GenerateManifest(dir, "1.0.0", "1.0.0", names, 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	manifest.TarSHA256 = strings.Repeat("a", 64)
	signTestManifest(t, manifest, 1)
	if err := WriteManifestAtomic(dir, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestReadSignedFileReturnsExactVerifiedBytesWithoutWrites(t *testing.T) {
	dir := signedReadFixture(t)
	before, err := os.ReadFile(filepath.Join(dir, ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	makeSyncDirReadOnly(t, dir)
	makeSyncDirReadOnly(t, filepath.Dir(dir))
	got, err := ReadSignedFile(dir, "ef-commission", "references/dispatch.md", int64(len(signedReadRule)))
	if err != nil || string(got) != signedReadRule {
		t.Fatalf("read signed file: %q %v", got, err)
	}
	after, err := os.ReadFile(filepath.Join(dir, ManifestFileName))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read mutated manifest")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(dir), lockFileName)); !os.IsNotExist(err) {
		t.Fatalf("read created a lock: %v", err)
	}
}

func TestReadSignedFileRejectsUnsignedMemberAndModifiedRelease(t *testing.T) {
	for _, mode := range []string{"unsigned-member", "changed-file", "changed-other-skill", "unsigned-manifest", "foreign-manager", "stale", "sync-lock", "swap-marker"} {
		t.Run(mode, func(t *testing.T) {
			dir := signedReadFixture(t)
			var err error
			switch mode {
			case "unsigned-member":
				writeSignedReadManifest(t, dir, []string{"ef-profile"})
			case "changed-file":
				err = os.WriteFile(filepath.Join(dir, "ef-commission", "references", "dispatch.md"), []byte("changed"), 0600)
			case "changed-other-skill":
				err = os.WriteFile(filepath.Join(dir, "ef-profile", "SKILL.md"), []byte("changed"), 0600)
			case "unsigned-manifest", "foreign-manager":
				manifest, readErr := ReadLocalManifest(dir)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if mode == "unsigned-manifest" {
					manifest.Signature = ""
				} else {
					manifest.ManagedBy = "somebody-else"
				}
				err = WriteManifestAtomic(dir, manifest)
			case "stale":
				err = os.WriteFile(filepath.Join(dir, StaleMarkerName), nil, 0600)
			case "sync-lock":
				err = os.WriteFile(filepath.Join(filepath.Dir(dir), lockFileName), []byte("sync"), 0600)
			case "swap-marker":
				err = os.WriteFile(dir+journalSuffix, []byte(dir+oldSuffix), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if data, err := ReadSignedFile(dir, "ef-commission", "references/dispatch.md", 4096); err == nil || data != nil {
				t.Fatalf("untrusted installation read: %q %v", data, err)
			}
		})
	}
}

func TestReadSignedFileRejectsUnsafePathAndBoundedNonText(t *testing.T) {
	dir := signedReadFixture(t)
	for _, path := range []string{"", ".", "../ef-profile/SKILL.md", "/references/dispatch.md", "references/../SKILL.md", "references\\dispatch.md", "references/dispatch.md:alternate", "references/.DS_Store", "._unsigned/dispatch.md"} {
		if data, err := ReadSignedFile(dir, "ef-commission", path, 4096); err == nil || data != nil {
			t.Fatalf("unsafe path %q read", path)
		}
	}
	for _, limit := range []int64{-1, 0, 5} {
		if data, err := ReadSignedFile(dir, "ef-commission", "references/dispatch.md", limit); err == nil || data != nil {
			t.Fatalf("invalid bound %d accepted", limit)
		}
	}
	if data, err := ReadSignedFile(dir, "../ef-commission", "references/dispatch.md", 4096); err == nil || data != nil {
		t.Fatal("unsafe Skill name read")
	}
	if err := os.WriteFile(filepath.Join(dir, "ef-commission", "references", "dispatch.md"), []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	writeSignedReadManifest(t, dir, []string{"ef-commission", "ef-profile"})
	if data, err := ReadSignedFile(dir, "ef-commission", "references/dispatch.md", 4096); err == nil || data != nil {
		t.Fatal("signed non-UTF-8 file read")
	}
}

func TestReadSignedFileRejectsSignedSymlinkPaths(t *testing.T) {
	for _, name := range []string{"file", "references", "skill", "manifest"} {
		t.Run(name, func(t *testing.T) {
			dir := signedReadFixture(t)
			path := filepath.Join(dir, "ef-commission", "references", "dispatch.md")
			switch name {
			case "references":
				path = filepath.Dir(path)
			case "skill":
				path = filepath.Join(dir, "ef-commission")
			case "manifest":
				path = filepath.Join(dir, ManifestFileName)
			}
			outside := filepath.Join(t.TempDir(), "target")
			if err := os.Rename(path, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if name != "manifest" {
				// This manifest genuinely signs the link string, not its target.
				writeSignedReadManifest(t, dir, []string{"ef-commission", "ef-profile"})
			}
			if data, err := ReadSignedFile(dir, "ef-commission", "references/dispatch.md", 4096); err == nil || data != nil {
				t.Fatalf("signed %s symlink was followed: %q %v", name, data, err)
			}
		})
	}
}
