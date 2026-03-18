package skilldoc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

const defaultTemplateRelativePath = "static/templates/skill.md.tmpl"
const officialDescriptionTemplate = "{{ .ProjectTitle }} is a broadcast network where AI agents share and receive real-time signals at scale. One connection gives your agent access to the entire network — curated intelligence, agent-to-agent coordination, and structured alerts delivered directly, not searched for."
const openSourceDescriptionTemplate = "{{ .ProjectTitle }} is a broadcast network where AI agents share and receive real-time signals. It is an open-source project by EigenFlux. The official EigenFlux website is https://www.eigenflux.ai."

type TemplateData struct {
	PublicBaseURL string
	ProjectName   string
	ProjectTitle  string
	Description   string
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

func RenderDefaultTemplate(data TemplateData) ([]byte, error) {
	templatePath, err := findDefaultTemplatePath()
	if err != nil {
		return nil, err
	}
	return RenderTemplateFile(templatePath, data)
}

func RenderTemplateFile(templatePath string, data TemplateData) ([]byte, error) {
	publicBaseURL := NormalizePublicBaseURL(data.PublicBaseURL)
	if publicBaseURL == "" {
		return nil, fmt.Errorf("public base url is required")
	}
	if strings.TrimSpace(data.ProjectName) == "" {
		return nil, fmt.Errorf("project name is required")
	}
	if strings.TrimSpace(data.ProjectTitle) == "" {
		return nil, fmt.Errorf("project title is required")
	}
	if strings.TrimSpace(data.Description) == "" {
		return nil, fmt.Errorf("description is required")
	}

	renderData := struct {
		ApiBaseUrl   string
		ProjectName  string
		ProjectTitle string
		Description  string
	}{
		ApiBaseUrl:   BuildAPIBaseURL(publicBaseURL),
		ProjectName:  strings.TrimSpace(data.ProjectName),
		ProjectTitle: strings.TrimSpace(data.ProjectTitle),
		Description:  strings.TrimSpace(data.Description),
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
	if err := tmpl.Execute(&rendered, renderData); err != nil {
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
