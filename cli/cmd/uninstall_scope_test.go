package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentScopeProtectsOtherHomesAndNestedSkills(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(base, ".eigenflux")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(home, "skills")
	other := filepath.Join(home, "nested", ".eigenflux")
	record := installationRecord{Homes: []installationHome{{Home: home, SkillsTarget: shared}, {Home: other, SkillsTarget: filepath.Join(shared, "nested")}}}
	plan, err := selectUninstallScope(record, home, "current")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Selected) != 1 || len(plan.Remaining.Homes) != 1 || !plan.SharedSkills[shared] {
		t.Fatalf("wrong current scope: %+v", plan)
	}
	for _, path := range []string{filepath.Join(other, "credentials.json"), filepath.Join(shared, "nested", "SKILL.md"), filepath.Join(home, "credentials.json")} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	o := uninstallObservation{Homes: plan.Selected, RetainedPaths: plan.RetainedPaths, RemovalScope: "current"}
	if err := o.purgeHomes(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(other, "credentials.json"), filepath.Join(shared, "nested", "SKILL.md")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("removed another Agent resource", err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("current credentials retained", err)
	}
}

func TestCurrentScopeKeepsExecutableInsideCurrentHome(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(base, ".eigenflux")
	bin := filepath.Join(home, "bin", "eigenflux")
	for _, path := range []string{bin, bin + ".install.json", bin + ".uninstall.sh", filepath.Join(home, "credentials.json")} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	o := uninstallObservation{Executable: bin, Homes: []installationHome{{Home: home}}, RemovalScope: "current"}
	if err := o.purgeHomes(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{bin, bin + ".install.json", bin + ".uninstall.sh"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("removed shared installation", err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("current credentials retained", err)
	}
}

func TestCurrentScopePreservesExternalSkillsWithoutOtherRegistrations(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(base, ".eigenflux")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(base, "shared-skills")
	private := filepath.Join(home, "skills")
	for _, target := range []string{external, private} {
		plan, err := selectUninstallScope(installationRecord{Homes: []installationHome{{Home: home, SkillsTarget: target}}}, home, "current")
		if err != nil {
			t.Fatal(err)
		}
		if plan.SharedSkills[target] != (target == external) {
			t.Fatalf("incorrect ownership decision for %s: %+v", target, plan)
		}
	}
}
