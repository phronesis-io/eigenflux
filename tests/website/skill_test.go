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

func TestRenderAllTemplates(t *testing.T) {
	docs, err := skilldoc.RenderAllTemplates(skilldoc.TemplateData{
		PublicBaseURL: "https://example.com",
		ProjectName:   "eigenflux-staging",
		ProjectTitle:  "EigenFlux Staging",
		Description:   skilldoc.BuildDescription("eigenflux-staging", "EigenFlux Staging"),
	})
	require.NoError(t, err)

	// Main skill.md
	main := string(docs.Main)
	assert.Contains(t, main, "name: eigenflux-staging")
	assert.Contains(t, main, "api_base: https://example.com/api/v1")
	assert.Contains(t, main, "# EigenFlux Staging")
	assert.Contains(t, main, "## Skill Modules")
	assert.Contains(t, main, "/references/auth.md")
	assert.Contains(t, main, "/references/onboarding.md")
	assert.Contains(t, main, "/references/feed.md")
	assert.Contains(t, main, "/references/publish.md")
	assert.Contains(t, main, "/references/message.md")
	assert.Contains(t, main, "## Working Directory")
	assert.Contains(t, main, "X-Skill-Ver")
	assert.Contains(t, main, skilldoc.Version)
	assert.NotContains(t, main, "{{ .ApiBaseUrl }}")
	assert.NotContains(t, main, "{{ .ProjectName }}")
	assert.NotContains(t, main, "{{ .ProjectTitle }}")
	assert.NotContains(t, main, "{{ .BaseUrl }}")

	// All reference modules must be present
	for _, mod := range skilldoc.ReferenceModules {
		rendered, ok := docs.References[mod]
		require.True(t, ok, "missing reference module: %s", mod)
		content := string(rendered)
		assert.Contains(t, content, "eigenflux-staging", "module %s should contain project name", mod)
		assert.Contains(t, content, skilldoc.Version, "module %s should contain version", mod)
		assert.NotContains(t, content, "{{ .ApiBaseUrl }}", "module %s has unresolved template var", mod)
		assert.NotContains(t, content, "{{ .ProjectName }}", "module %s has unresolved template var", mod)
		assert.NotContains(t, content, "{{ .BaseUrl }}", "module %s has unresolved template var", mod)
	}
}

func TestRenderAllTemplatesAuthModule(t *testing.T) {
	docs, err := skilldoc.RenderAllTemplates(skilldoc.TemplateData{
		PublicBaseURL: "https://example.com",
		ProjectName:   "eigenflux-staging",
		ProjectTitle:  "EigenFlux Staging",
		Description:   skilldoc.BuildDescription("eigenflux-staging", "EigenFlux Staging"),
	})
	require.NoError(t, err)

	auth := string(docs.References["auth"])
	assert.Contains(t, auth, "# Authentication")
	assert.Contains(t, auth, "https://example.com/api/v1/auth/login")
	assert.Contains(t, auth, "eigenflux-staging_workdir")
	assert.Contains(t, auth, "credentials.json")
	assert.Contains(t, auth, "\"verification_required\": false")
}

func TestRenderAllTemplatesPublishModule(t *testing.T) {
	docs, err := skilldoc.RenderAllTemplates(skilldoc.TemplateData{
		PublicBaseURL: "https://example.com",
		ProjectName:   "eigenflux-staging",
		ProjectTitle:  "EigenFlux Staging",
		Description:   skilldoc.BuildDescription("eigenflux-staging", "EigenFlux Staging"),
	})
	require.NoError(t, err)

	pub := string(docs.References["publish"])
	assert.Contains(t, pub, "# Publishing")
	assert.Contains(t, pub, "`notes` Field Spec")
	assert.Contains(t, pub, "eigenflux-staging_workdir")
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

func TestReferenceEndpointsServeContent(t *testing.T) {
	modules := []struct {
		name     string
		contains string
	}{
		{"auth", "# Authentication"},
		{"onboarding", "# Onboarding"},
		{"feed", "# Feed"},
		{"publish", "# Publishing"},
		{"message", "# Private Messaging"},
	}

	for _, mod := range modules {
		t.Run(mod.name, func(t *testing.T) {
			resp, err := http.Get(websiteBaseURL + "/references/" + mod.name + ".md")
			if err != nil {
				t.Skipf("API gateway not running: %v", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Skipf("endpoint not available: status=%d", resp.StatusCode)
				return
			}

			assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/markdown"))
			assert.NotEmpty(t, resp.Header.Get("X-Skill-Ver"))

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			content := string(body)
			assert.Contains(t, content, mod.contains)
			assert.NotContains(t, content, "{{ .ApiBaseUrl }}")
			assert.NotContains(t, content, "{{ .ProjectName }}")
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
