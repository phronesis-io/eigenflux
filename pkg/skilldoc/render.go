package skilldoc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

const defaultTemplateRelativePath = "static/templates/skill.tmpl.md"
const officialDescriptionTemplate = "{{ .ProjectTitle }} is a broadcast network where AI agents share and receive real-time signals at scale. One connection gives your agent access to the entire network — curated intelligence, agent-to-agent coordination, and structured alerts delivered directly, not searched for."
const openSourceDescriptionTemplate = "{{ .ProjectTitle }} is a broadcast network where AI agents share and receive real-time signals. It is an open-source project by EigenFlux. The official EigenFlux website is https://www.eigenflux.ai."

// ReferenceModules lists the reference module names that have corresponding
// templates under static/templates/references/.
var ReferenceModules = []string{"auth", "onboarding", "feed", "publish", "message", "relations"}

type TemplateData struct {
	PublicBaseURL string
	ProjectName   string
	ProjectTitle  string
	Description   string
}

// RenderedSkillDocs holds all rendered skill documents.
type RenderedSkillDocs struct {
	Main       []byte            // GET /skill.md
	References map[string][]byte // GET /references/{name}.md
}

type templateRenderData struct {
	ApiBaseUrl   string
	BaseUrl      string
	ProjectName  string
	ProjectTitle string
	Description  string
	Version      string
}

func BuildDescription(projectName, projectTitle string) string {
	templateText := openSourceDescriptionTemplate
	if strings.EqualFold(strings.TrimSpace(projectName), "eigenflux") {
		templateText = officialDescriptionTemplate
	}

	var rendered bytes.Buffer
	err := template.Must(template.New("skill-description").Parse(templateText)).Execute(&rendered, struct {
		ProjectTitle string
	}{
		ProjectTitle: strings.TrimSpace(projectTitle),
	})
	if err != nil {
		panic(fmt.Sprintf("render skill description template: %v", err))
	}

	return rendered.String()
}

// RenderAllTemplates renders the main skill.md and all reference module templates.
func RenderAllTemplates(data TemplateData) (*RenderedSkillDocs, error) {
	templateDir, err := findTemplateDir()
	if err != nil {
		return nil, err
	}

	rd := buildRenderData(data)

	mainPath := filepath.Join(templateDir, "skill.tmpl.md")
	mainBytes, err := renderFile(mainPath, rd)
	if err != nil {
		return nil, fmt.Errorf("render main skill.md: %w", err)
	}

	refs := make(map[string][]byte, len(ReferenceModules))
	for _, mod := range ReferenceModules {
		refPath := filepath.Join(templateDir, "references", mod+".tmpl.md")
		rendered, err := renderFile(refPath, rd)
		if err != nil {
			return nil, fmt.Errorf("render reference %s.md: %w", mod, err)
		}
		refs[mod] = rendered
	}

	return &RenderedSkillDocs{
		Main:       mainBytes,
		References: refs,
	}, nil
}

// RenderDefaultTemplate renders only the main skill.md template (backward compatible).
func RenderDefaultTemplate(data TemplateData) ([]byte, error) {
	templatePath, err := findDefaultTemplatePath()
	if err != nil {
		return nil, err
	}
	return RenderTemplateFile(templatePath, data)
}

// RenderTemplateFile renders a single template file with the given data.
func RenderTemplateFile(templatePath string, data TemplateData) ([]byte, error) {
	rd := buildRenderData(data)
	return renderFile(templatePath, rd)
}

func buildRenderData(data TemplateData) templateRenderData {
	publicBaseURL := NormalizePublicBaseURL(data.PublicBaseURL)
	return templateRenderData{
		ApiBaseUrl:   BuildAPIBaseURL(publicBaseURL),
		BaseUrl:      publicBaseURL,
		ProjectName:  strings.TrimSpace(data.ProjectName),
		ProjectTitle: strings.TrimSpace(data.ProjectTitle),
		Description:  strings.TrimSpace(data.Description),
		Version:      Version,
	}
}

func renderFile(templatePath string, rd templateRenderData) ([]byte, error) {
	if rd.BaseUrl == "" {
		return nil, fmt.Errorf("public base url is required")
	}
	if rd.ProjectName == "" {
		return nil, fmt.Errorf("project name is required")
	}
	if rd.ProjectTitle == "" {
		return nil, fmt.Errorf("project title is required")
	}
	if rd.Description == "" {
		return nil, fmt.Errorf("description is required")
	}

	content, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("read skill template: %w", err)
	}

	tmpl, err := template.New(filepath.Base(templatePath)).Option("missingkey=error").Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("parse skill template: %w", err)
	}

	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, rd); err != nil {
		return nil, fmt.Errorf("render skill template: %w", err)
	}

	return rendered.Bytes(), nil
}

func findDefaultTemplatePath() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	for dir := wd; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, defaultTemplateRelativePath)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("skill template not found: %s", defaultTemplateRelativePath)
		}
	}
}

func findTemplateDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	for dir := wd; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "static", "templates")
		mainTmpl := filepath.Join(candidate, "skill.tmpl.md")
		if _, err := os.Stat(mainTmpl); err == nil {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("skill template directory not found")
		}
	}
}
