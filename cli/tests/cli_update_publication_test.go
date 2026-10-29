package tests

import (
	"os"
	"strings"
	"testing"
)

func TestSkillsPublicationRequiresAvailableCLI(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	script := read("../scripts/release-skills.sh")
	gate := strings.Index(script, "./cmd/clirelease")
	reserve := strings.Index(script, "skills-release-state.py\" reserve")
	if gate < 0 || reserve < gate || !strings.Contains(script, `--min-version "${SKILLS_MIN_CLI_VERSION:-$CLI_VERSION}"`) {
		t.Fatal("Skills must verify the published CLI minimum before reserving or publishing")
	}
	cliWorkflow := read("../../.github/workflows/release-cli.yml")
	skillsWorkflow := read("../../.github/workflows/release-skills.yml")
	for _, workflow := range []string{cliWorkflow, skillsWorkflow} {
		if !strings.Contains(workflow, "group: release-r2") || !strings.Contains(workflow, "cancel-in-progress: false") {
			t.Fatal("publishers must share serialization")
		}
	}
	for _, required := range []string{"workflow_run:", "workflows: [Release CLI]", "types: [completed]", "github.event.workflow_run.conclusion == 'success'"} {
		if !strings.Contains(skillsWorkflow, required) {
			t.Fatalf("missing successful CLI handoff: %s", required)
		}
	}
	publish := read("../scripts/publish.sh")
	if strings.Index(publish, `bash "$SCRIPT_DIR/release-skills.sh"`) < strings.Index(publish, `"s3://$R2_BUCKET/cli/latest/release.json"`) {
		t.Fatal("Skills dispatched before CLI pointer publication")
	}
}
