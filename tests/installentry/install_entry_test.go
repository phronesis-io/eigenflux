package installentry_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"eigenflux_server/pkg/skilldoc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAGTIInstallationHandoff(t *testing.T) {
	const installURL = "https://github.com/phronesis-io/eigenflux/blob/main/skills/install.md"
	for _, name := range []string{"agti_skills.tmpl.md", "agti_join.tmpl.md", "agti_interpret.tmpl.md"} {
		for _, ref := range []string{"", "campaign_01"} {
			t.Run(name+"/"+ref, func(t *testing.T) {
				tmpl, err := template.ParseFiles(filepath.Join("..", "..", "static", "templates", name))
				require.NoError(t, err)
				var rendered bytes.Buffer
				err = tmpl.Execute(&rendered, map[string]interface{}{
					"BaseUrl": "https://www.eigenflux.net", "Ref": ref,
					"RefQuery": "", "TypeName": "Test", "Match": 4, "Total": 10,
					"AgentName": "Test Agent", "ModelName": "", "Compare": []interface{}{},
				})
				require.NoError(t, err)
				doc := rendered.String()
				assert.Contains(t, doc, installURL)
				assert.NotContains(t, doc, "Read https://github.com/phronesis-io/eigenflux and")
				assert.NotContains(t, doc, "curl -fsSL")
				assert.NotContains(t, doc, "eigenflux agent provision")
				assert.NotContains(t, doc, "--ref "+ref)
				assert.NotContains(t, doc, "我已经上网络了")
				assert.NotContains(t, doc, "<no value>")
				if ref == "" {
					assert.NotContains(t, doc, "/agti/join/")
				} else if name == "agti_join.tmpl.md" {
					assert.Contains(t, doc, "`"+ref+"`")
					assert.NotContains(t, doc, "/agti/join/", "the attribution handoff must not recurse")
				} else {
					attributionURL := "https://www.eigenflux.net/agti/join/" + ref
					assert.Contains(t, doc, attributionURL)
					assert.Contains(t, doc, "once for AGTI attribution")
					assert.Less(t, strings.Index(doc, installURL), strings.Index(doc, attributionURL))
				}
			})
		}
	}
}

func TestCompatibilityEntriesDelegateInstallation(t *testing.T) {
	const installURL = "https://github.com/phronesis-io/eigenflux/blob/main/skills/install.md"
	for _, origin := range []string{"https://www.eigenflux.ai", "https://example.com/hub"} {
		doc, err := skilldoc.RenderDefaultTemplate(skilldoc.TemplateData{
			PublicBaseURL: origin, ProjectName: "test-hub", ProjectTitle: "Test Hub", Description: "Test network",
		})
		require.NoError(t, err)
		assert.Contains(t, string(doc), installURL)
		assert.Contains(t, string(doc), "Installer origin: `"+origin+"`")
		assert.Contains(t, string(doc), "ef-onboarding")
		assert.Contains(t, string(doc), "ef-profile")
		assert.NotContains(t, string(doc), "curl -fsSL")
		assert.NotContains(t, string(doc), "eigenflux agent provision")
	}
	bootstrap, err := os.ReadFile(filepath.Join("..", "..", "static", "BOOTSTRAP.md"))
	require.NoError(t, err)
	assert.Contains(t, string(bootstrap), installURL)
	assert.NotContains(t, string(bootstrap), "/skill.md")
	assert.NotContains(t, string(bootstrap), "openclaw plugins install")
}
