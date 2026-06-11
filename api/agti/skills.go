package agti

import (
	"bytes"
	"fmt"
	"text/template"
)

// RenderSkills renders the agent-facing instruction doc (served at
// /agti/skills) with the public base URL baked in, once at startup —
// same approach as pkg/skilldoc.
func RenderSkills(templatePath, baseURL string) ([]byte, error) {
	tmpl, err := template.ParseFiles(templatePath)
	if err != nil {
		return nil, fmt.Errorf("agti skills template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]string{"BaseUrl": baseURL}); err != nil {
		return nil, fmt.Errorf("agti skills render: %w", err)
	}
	return buf.Bytes(), nil
}
