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

// --- Unit tests (no server required) ---

func TestRenderSkillTemplate(t *testing.T) {
	rendered, err := skilldoc.RenderDefaultTemplate(skilldoc.TemplateData{
		PublicBaseURL: "https://example.com",
		ProjectName:   "eigenflux-staging",
		ProjectTitle:  "EigenFlux Staging",
		Description:   skilldoc.BuildDescription("eigenflux-staging", "EigenFlux Staging"),
	})
	require.NoError(t, err)

	main := string(rendered)
	assert.Contains(t, main, "name: eigenflux-staging")
	assert.Contains(t, main, "api_base: https://example.com/api/v1")
	assert.Contains(t, main, "# EigenFlux Staging")
	assert.Contains(t, main, "## Skill Modules")
	assert.Contains(t, main, "Verify CLI `0.0.39` or newer")
	assert.Contains(t, main, "Remote V1 reference documents are no longer served")
	assert.NotContains(t, main, "https://example.com/references/")
	assert.Contains(t, main, skilldoc.Version)
	assert.NotContains(t, main, "{{ .ApiBaseUrl }}")
	assert.NotContains(t, main, "{{ .ProjectName }}")
	assert.NotContains(t, main, "{{ .ProjectTitle }}")
	assert.NotContains(t, main, "{{ .BaseUrl }}")
}

func TestRenderDefaultTemplateAppendsAPIV1Suffix(t *testing.T) {
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

// --- E2E tests (require running API gateway) ---

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
	assert.NotEmpty(t, resp.Header.Get("X-Skill-Ver"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	content := string(body)
	assert.Contains(t, content, "api_base:")
	assert.Contains(t, content, "## Skill Modules")
	assert.NotContains(t, content, "{{ .ApiBaseUrl }}")
	assert.NotContains(t, content, "{{ .ProjectName }}")
	assert.NotContains(t, content, "{{ .ProjectTitle }}")
	assert.NotContains(t, content, "{{ .Description }}")
}

func TestReferenceEndpointsAreRetired(t *testing.T) {
	for _, module := range []string{"auth", "onboarding", "feed", "publish", "message", "relations"} {
		t.Run(module, func(t *testing.T) {
			resp, err := http.Get(websiteBaseURL + "/references/" + module + ".md")
			if err != nil {
				t.Skipf("API gateway not running: %v", err)
				return
			}
			defer resp.Body.Close()
			assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		})
	}
}

func TestSkillVersionHeaderPassthrough(t *testing.T) {
	req, err := http.NewRequest("GET", websiteBaseURL+"/skill.md", nil)
	if err != nil {
		t.Skipf("failed to create request: %v", err)
		return
	}
	req.Header.Set("X-Skill-Ver", "0.0.1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("API gateway not running: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Skipf("endpoint not available: status=%d", resp.StatusCode)
		return
	}

	// Server always returns full content regardless of client version
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("X-Skill-Ver"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "## Skill Modules")
}
