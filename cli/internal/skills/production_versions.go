package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var stableSkillVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// ValidateProductionSkillVersions rejects invalid release metadata before signing.
// Development-only and unrelated Skills follow DiscoverProductionSkills exclusions.
func ValidateProductionSkillVersions(srcDir string) error {
	names, err := DiscoverProductionSkills(srcDir)
	if err != nil {
		return err
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(srcDir, name, "SKILL.md"))
		if err != nil {
			return fmt.Errorf("read %s version: %w", name, err)
		}
		version := productionMetadataVersion(string(data))
		if version != readDisplayVersion(filepath.Join(srcDir, name, "SKILL.md")) {
			return fmt.Errorf("production Skill %s has ambiguous version metadata", name)
		}
		if !stableSkillVersion.MatchString(version) {
			return fmt.Errorf("production Skill %s metadata.version %q must use stable major.minor.patch", name, version)
		}
	}
	return nil
}

func productionMetadataVersion(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasPrefix(body, "---\n") {
		return ""
	}
	frontmatter, _, ok := strings.Cut(body[4:], "\n---")
	if !ok {
		return ""
	}
	inMetadata := false
	version := ""
	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if line == trimmed {
			inMetadata = trimmed == "metadata:"
			continue
		}
		if inMetadata {
			if value, ok := strings.CutPrefix(line, "  version:"); ok {
				if version != "" {
					return ""
				}
				version = strings.TrimSpace(value)
				if len(version) >= 2 && ((version[0] == '"' && version[len(version)-1] == '"') || (version[0] == '\'' && version[len(version)-1] == '\'')) {
					version = version[1 : len(version)-1]
				}
			}
		}
	}
	return version
}
