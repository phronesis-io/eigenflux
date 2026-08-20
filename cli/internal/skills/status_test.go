package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListLocalReportsLoadedVersionForModifiedSkill(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "ef-profile")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	writeSkill := func(version string) {
		t.Helper()
		body := []byte("---\nname: ef-profile\nmetadata:\n  version: \"" + version + "\"\n---\n")
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), body, 0o600); err != nil {
			t.Fatalf("write skill: %v", err)
		}
	}

	writeSkill("0.3.1")
	manifest, err := GenerateManifest(dir, "0.0.33", "", []string{"ef-profile"}, 1)
	if err != nil {
		t.Fatalf("generate manifest: %v", err)
	}
	if err := WriteManifestAtomic(dir, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	writeSkill("0.4.0-dev.3")
	_, installed, managed, err := ListLocal(dir, "")
	if err != nil {
		t.Fatalf("list local: %v", err)
	}
	if !managed {
		t.Fatal("expected managed skills directory")
	}
	if len(installed) != 1 {
		t.Fatalf("installed skills = %d, want 1", len(installed))
	}
	if installed[0].SHAMatch {
		t.Fatal("modified skill unexpectedly matches manifest")
	}
	if got := installed[0].DisplayVersion; got != "0.4.0-dev.3" {
		t.Fatalf("display version = %q, want loaded local version", got)
	}
}
