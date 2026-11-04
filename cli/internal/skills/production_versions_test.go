package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProductionSkillVersions(t *testing.T) {
	for _, version := range []string{"0.0.0", "0.4.0", "12.34.56", "999.0.36", "", "1.2", "01.2.3", "1.02.3", "1.2.03", "1.2.3-dev.1", "1.2.3+build", "v1.2.3", "1.2.3.4"} {
		for _, ending := range []string{"\n", "\r\n"} {
			t.Run(version+"/"+ending, func(t *testing.T) {
				root := t.TempDir()
				for _, name := range []string{"ef-future", "ef-localdev", "third-party"} {
					dir := filepath.Join(root, name)
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					value := version
					if name != "ef-future" {
						value = "dev"
					}
					body := "---\nname: " + name + "\nmetadata:\n  author: test\n  version: \"" + value + "\"\n---\n"
					if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(strings.ReplaceAll(body, "\n", ending)), 0600); err != nil {
						t.Fatal(err)
					}
				}
				err := ValidateProductionSkillVersions(root)
				valid := version == "0.0.0" || version == "0.4.0" || version == "12.34.56" || version == "999.0.36"
				if (err == nil) != valid {
					t.Fatalf("version %q: error=%v valid=%v", version, err, valid)
				}
				if err != nil && !strings.Contains(err.Error(), "ef-future") {
					t.Fatalf("missing Skill name: %v", err)
				}
			})
		}
	}
}

func TestProductionMetadataVersionScope(t *testing.T) {
	cases := []struct{ body, want string }{
		{"---\nmetadata:\n  version: '1.2.3'\n---\n", "1.2.3"},
		{"---\nmetadata:\n  version: 1.2.3\n---\n", "1.2.3"},
		{"---\nversion: 1.2.3\n---\n", ""},
		{"---\nname: ef-future\n---\nmetadata:\n  version: 1.2.3\n", ""},
		{"metadata:\n  version: 1.2.3\n", ""},
		{"---\nmetadata:\n  author: test\n---\n  version: 1.2.3\n", ""},
		{"---\nmetadata:\n  version: 1.2.3\n  version: 2.3.4\n---\n", ""},
	}
	for _, c := range cases {
		if got := productionMetadataVersion(c.body); got != c.want {
			t.Errorf("%q: got %q want %q", c.body, got, c.want)
		}
	}
}
