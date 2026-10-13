package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUninstallRemovesEditedSkillsAndPreservesUnmanaged(t *testing.T) {
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
	if err != nil || len(preview.Removed) != 2 || len(preview.Preserved) != 0 {
		t.Fatalf("%+v %v", preview, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ef-one")); err != nil {
		t.Fatal("preview removed files")
	}
	result, err := Uninstall(dir, true)
	if err != nil || len(result.Removed) != 2 || len(result.Preserved) != 0 {
		t.Fatalf("%+v %v", result, err)
	}
	for _, name := range []string{"ef-one", "ef-two"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("owned skill remains: %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "unrelated", "SKILL.md")); err != nil {
		t.Fatal("unmanaged skill removed", err)
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

func TestUninstallInstallationRetainsEvidenceForRetry(t *testing.T) {
	target := filepath.Join(t.TempDir(), "skills")
	_ = os.MkdirAll(filepath.Join(target, "ef-one"), 0700)
	_ = os.WriteFile(filepath.Join(target, "ef-one", "SKILL.md"), []byte("one"), 0600)
	digest, _ := dirSHA256(filepath.Join(target, "ef-one"))
	if err := WriteManifestAtomic(target, &Manifest{ManagedBy: ManagedByValue, Skills: []SkillEntry{{Name: "ef-one", SHA256: digest}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(target, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "ef-one")); !os.IsNotExist(err) {
		t.Fatalf("skill not removed: %v", err)
	}
	for _, apply := range []bool{false, true} {
		r, err := Uninstall(target, apply, true)
		if err != nil || len(r.Removed) != 0 || len(r.Preserved) != 0 {
			t.Fatalf("retry failed: %+v %v", r, err)
		}
	}
	unknown := filepath.Join(t.TempDir(), "skills")
	_ = os.MkdirAll(unknown, 0700)
	if _, err := Uninstall(unknown, true, true); err == nil {
		t.Fatal("arbitrary unowned target was accepted")
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

func TestUninstallUnlinksSkillWithoutFollowingAndRejectsLinkedManifest(t *testing.T) {
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
	r, err := Uninstall(target, true, true)
	if err != nil || len(r.Preserved) != 0 || len(r.Removed) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Lstat(filepath.Join(target, "ef-one")); !os.IsNotExist(err) {
		t.Fatalf("managed symlink remains: %v", err)
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

func TestUninstallMissingTargetIsAlreadyCleaned(t *testing.T) {
	target := filepath.Join(t.TempDir(), "missing")
	for _, apply := range []bool{false, true} {
		result, err := Uninstall(target, apply)
		if err != nil || result == nil || len(result.Removed) != 0 {
			t.Fatalf("missing target: %+v %v", result, err)
		}
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(target, true); err == nil {
		t.Fatal("existing unowned directory accepted")
	}
}
