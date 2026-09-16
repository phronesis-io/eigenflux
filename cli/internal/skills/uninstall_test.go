package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUninstallPreservesEditedAndUnmanaged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	_ = os.MkdirAll(dir, 0700)
	for _, name := range []string{"ef-one", "ef-two", "unrelated"} {
		_ = os.MkdirAll(filepath.Join(dir, name), 0700)
		_ = os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(name), 0600)
	}
	one, _ := dirSHA256(filepath.Join(dir, "ef-one"))
	two, _ := dirSHA256(filepath.Join(dir, "ef-two"))
	m := &Manifest{ManagedBy: ManagedByValue, Skills: []SkillEntry{{Name: "ef-one", SHA256: one}, {Name: "ef-two", SHA256: two}}}
	if err := WriteManifestAtomic(dir, m); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "ef-two", "SKILL.md"), []byte("user edit"), 0600)
	preview, err := Uninstall(dir, false)
	if err != nil || len(preview.Removed) != 1 || len(preview.Preserved) != 1 {
		t.Fatalf("%+v %v", preview, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ef-one")); err != nil {
		t.Fatal("preview removed files")
	}
	result, err := Uninstall(dir, true)
	if err != nil || len(result.Removed) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	for _, name := range []string{"ef-two", "unrelated"} {
		if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUninstallPreviewDoesNotTouchStaleLock(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "skills")
	_ = os.MkdirAll(target, 0700)
	_ = os.MkdirAll(filepath.Join(target, "ef-one"), 0700)
	_ = os.WriteFile(filepath.Join(target, "ef-one", "SKILL.md"), []byte("one"), 0600)
	digest, _ := dirSHA256(filepath.Join(target, "ef-one"))
	if err := WriteManifestAtomic(target, &Manifest{ManagedBy: ManagedByValue, Skills: []SkillEntry{{Name: "ef-one", SHA256: digest}}}); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(parent, lockFileName)
	original := []byte("123 1\n")
	_ = os.WriteFile(lockPath, original, 0600)
	before, _ := os.Stat(lockPath)
	if _, err := Uninstall(target, false); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(lockPath)
	body, _ := os.ReadFile(lockPath)
	if err != nil || string(body) != string(original) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("preview mutated stale lock")
	}
}

func TestUninstallValidatesWholeManifestBeforeRemoval(t *testing.T) {
	target := filepath.Join(t.TempDir(), "skills")
	_ = os.MkdirAll(filepath.Join(target, "ef-one"), 0700)
	_ = os.WriteFile(filepath.Join(target, "ef-one", "SKILL.md"), []byte("one"), 0600)
	digest, _ := dirSHA256(filepath.Join(target, "ef-one"))
	m := &Manifest{ManagedBy: ManagedByValue, Skills: []SkillEntry{{Name: "ef-one", SHA256: digest}, {Name: "../outside", SHA256: digest}}}
	if err := WriteManifestAtomic(target, m); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(target, true); err == nil {
		t.Fatal("invalid manifest accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "ef-one", "SKILL.md")); err != nil {
		t.Fatal("partial removal before validation")
	}
}

func TestUninstallRetainsLinkedSkillAndRejectsLinkedManifest(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "skills")
	outside := filepath.Join(parent, "outside")
	_ = os.MkdirAll(target, 0700)
	_ = os.MkdirAll(outside, 0700)
	_ = os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("keep"), 0600)
	if err := os.Symlink(outside, filepath.Join(target, "ef-one")); err != nil {
		t.Skip(err)
	}
	digest, _ := dirSHA256(outside)
	if err := WriteManifestAtomic(target, &Manifest{ManagedBy: ManagedByValue, Skills: []SkillEntry{{Name: "ef-one", SHA256: digest}}}); err != nil {
		t.Fatal(err)
	}
	r, err := Uninstall(target, true)
	if err != nil || len(r.Preserved) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "SKILL.md")); err != nil || string(data) != "keep" {
		t.Fatal("linked content changed")
	}
	manifest := filepath.Join(target, ManifestFileName)
	moved := filepath.Join(parent, "foreign-manifest")
	_ = os.Rename(manifest, moved)
	_ = os.Symlink(moved, manifest)
	if _, err := Uninstall(target, true); err == nil {
		t.Fatal("linked manifest accepted")
	}
}
