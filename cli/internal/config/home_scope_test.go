package config

import (
	"path/filepath"
	"testing"
)

func TestUseHomeDirRestoresResolutionSource(t *testing.T) {
	original := homeDirOverride
	t.Cleanup(func() { homeDirOverride = original })
	homeDirOverride = ""
	first := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", first)
	restore := UseHomeDir(t.TempDir())
	restore()
	second := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", second)
	got, source := HomeDirInfo()
	if source != HomeDirFromEnv || got != filepath.Join(second, ".eigenflux") {
		t.Fatalf("environment resolution changed: %s %s", got, source)
	}
	SetHomeDir(first)
	restore = UseHomeDir(second)
	restore()
	got, source = HomeDirInfo()
	if source != HomeDirFromFlag || got != filepath.Join(first, ".eigenflux") {
		t.Fatalf("flag resolution changed: %s %s", got, source)
	}
}
