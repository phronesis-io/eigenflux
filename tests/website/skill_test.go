package website_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"eigenflux_server/pkg/skilldoc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderSkillTemplateUsesConfiguredValues(t *testing.T) {
	rendered, err := skilldoc.RenderDefaultTemplate(skilldoc.TemplateData{
		PublicBaseURL: "https://example.com",
		ProjectName:   "eigenflux-staging",
		ProjectTitle:  "EigenFlux Staging",
		Description:   skilldoc.BuildDescription("eigenflux-staging", "EigenFlux Staging"),
	})
	require.NoError(t, err)

	content := string(rendered)
	assert.Contains(t, content, "name: eigenflux-staging")
	assert.Contains(t, content, "api_base: https://example.com/api/v1")
	assert.Contains(t, content, "# EigenFlux Staging")
	assert.Contains(t, content, "`eigenflux-staging/credentials.json`")
	assert.Contains(t, content, "`~/.openclaw/eigenflux-staging/credentials.json`")
	assert.Contains(t, content, "Exclude `eigenflux-staging/` from version control")
	assert.Contains(t, content, "\"verification_required\": false")
	assert.Contains(t, content, "Only do this step when Step 1 did not return `access_token` and `verification_required=true`.")
	assert.Contains(t, content, "`POST /api/v1/auth/login/verify` (optional, only when login returns `verification_required=true`)")
	assert.Contains(t, content, "EigenFlux Staging is a broadcast network where AI agents share and receive real-time signals.")
	assert.Contains(t, content, "It is an open-source project by EigenFlux. The official EigenFlux website is https://www.eigenflux.ai.")
	assert.NotContains(t, content, "{{ .ApiBaseUrl }}")
	assert.NotContains(t, content, "{{ .ProjectName }}")
	assert.NotContains(t, content, "{{ .ProjectTitle }}")
	assert.NotContains(t, content, "{{ .Description }}")
}

func TestRenderSkillTemplateAppendsAPIV1Suffix(t *testing.T) {
	rendered, err := skilldoc.RenderDefaultTemplate(skilldoc.TemplateData{
		PublicBaseURL: "https://example.com/root/api/v1",
		ProjectName:   "eigenflux-staging",
		ProjectTitle:  "EigenFlux Staging",
		Description:   skilldoc.BuildDescription("eigenflux-staging", "EigenFlux Staging"),
	})
	require.NoError(t, err)

	content := string(rendered)
	assert.Contains(t, content, "api_base: https://example.com/root/api/v1")
}

func TestBuildDescriptionUsesOfficialCopyForEigenFlux(t *testing.T) {
	description := skilldoc.BuildDescription("eigenflux", "EigenFlux")

	assert.Equal(t, "EigenFlux is a broadcast network where AI agents share and receive real-time signals at scale. One connection gives your agent access to the entire network — curated intelligence, agent-to-agent coordination, and structured alerts delivered directly, not searched for.", description)
}

func TestSkillEndpointServesRenderedContent(t *testing.T) {
	resp, err := http.Get(websiteBaseURL + "/skill.md")
	if err != nil {
		t.Skipf("API gateway not running: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Skipf("API gateway not serving /skill.md yet: status=%d", resp.StatusCode)
		return
	}
	assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/markdown"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	content := string(body)
	assert.Contains(t, content, "api_base:")
	assert.Contains(t, content, "credentials.json")
	assert.NotContains(t, content, "{{ .ApiBaseUrl }}")
	assert.NotContains(t, content, "{{ .ProjectName }}")
	assert.NotContains(t, content, "{{ .ProjectTitle }}")
	assert.NotContains(t, content, "{{ .Description }}")
}
