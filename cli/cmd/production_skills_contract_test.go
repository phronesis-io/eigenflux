package cmd

import (
	"cli.eigenflux.ai/internal/skills"
	"path/filepath"
	"testing"
)

func TestProductionSkillsUseStableVersions(t *testing.T) {
	if err := skills.ValidateProductionSkillVersions(filepath.Join("..", "..", "skills")); err != nil {
		t.Fatal(err)
	}
}
