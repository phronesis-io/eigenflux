package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"cli.eigenflux.ai/internal/config"
)

func TestDiscoverProductionSkillsIsExtensible(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"ef-broadcast", "ef-future", "ef-localdev", "other-skill", "ef-missing"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ef-broadcast", "ef-future", "ef-localdev", "other-skill"} {
		if err := os.WriteFile(filepath.Join(root, name, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := DiscoverProductionSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ef-broadcast", "ef-future"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverProductionSkills() = %v, want %v", got, want)
	}
}

func TestResolveSkillsDirPrecedence(t *testing.T) {
	base := t.TempDir()
	config.SetHomeDir(filepath.Join(base, "agent-home"))
	t.Cleanup(func() { config.SetHomeDir("") })
	t.Setenv("HOME", filepath.Join(base, "user"))
	t.Setenv("EIGENFLUX_SKILLS_DIR", "")

	registered := filepath.Join(base, "registered")
	if _, err := RegisterTarget(config.HomeDir(), registered, "workbuddy"); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveSkillsDir("", "claude-code")
	if err != nil || got != registered {
		t.Fatalf("registered target = %q, %v; want %q", got, err, registered)
	}

	environment := filepath.Join(base, "environment")
	t.Setenv("EIGENFLUX_SKILLS_DIR", environment)
	got, err = ResolveSkillsDir("", "claude-code")
	if err != nil || got != environment {
		t.Fatalf("environment target = %q, %v; want %q", got, err, environment)
	}

	explicit := filepath.Join(base, "explicit")
	got, err = ResolveSkillsDir(explicit, "claude-code")
	if err != nil || got != explicit {
		t.Fatalf("explicit target = %q, %v; want %q", got, err, explicit)
	}
}

func TestTargetRegistrationRejectsRelativePersistedPath(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, targetRegistryFile), []byte(`{"path":"relative"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTargetRegistration(home); err == nil {
		t.Fatal("relative registered target was accepted")
	}
}

func TestKnownAgentHostsResolveWithoutScanning(t *testing.T) {
	base := t.TempDir()
	config.SetHomeDir(filepath.Join(base, "agent-home"))
	t.Cleanup(func() { config.SetHomeDir("") })
	t.Setenv("HOME", filepath.Join(base, "user"))
	t.Setenv("EIGENFLUX_SKILLS_DIR", "")
	for _, host := range []string{"workbuddy/5.3.14", "hermes/0.20.0", "codex/1.0", "openclaw/1.0"} {
		got, err := ResolveSkillsDir("", host)
		want := filepath.Join(base, "user", ".agents", "skills")
		if err != nil || got != want {
			t.Fatalf("ResolveSkillsDir(%q) = %q, %v; want %q", host, got, err, want)
		}
	}
}
