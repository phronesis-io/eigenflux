package agti

import (
	"bytes"
	"fmt"
	"text/template"
)

// Skill / join docs are rendered per request so a KOL ref (01..50) can be baked
// into the documented API call and join link — that's how the ref rides through
// the funnel without asking the agent to remember a tracking code.
var (
	skillTmpl *template.Template
	joinTmpl  *template.Template
	skillBase string
)

// InitSkills parses the agent-facing skill + join templates once at startup.
func InitSkills(skillPath, joinPath, baseURL string) error {
	st, err := template.ParseFiles(skillPath)
	if err != nil {
		return fmt.Errorf("agti skills template: %w", err)
	}
	jt, err := template.ParseFiles(joinPath)
	if err != nil {
		return fmt.Errorf("agti join template: %w", err)
	}
	skillTmpl = st
	joinTmpl = jt
	skillBase = baseURL
	return nil
}

// tmplData builds the render context. RefQuery is appended to the quiz/new URL
// (e.g. "?ref=01"); Ref drives the {{ if .Ref }} branches.
func tmplData(ref string) map[string]string {
	q := ""
	if ref != "" {
		q = "?ref=" + ref
	}
	return map[string]string{"BaseUrl": skillBase, "Ref": ref, "RefQuery": q}
}

func renderSkill(ref string) []byte {
	var buf bytes.Buffer
	_ = skillTmpl.Execute(&buf, tmplData(ref))
	return buf.Bytes()
}

func renderJoin(ref string) []byte {
	var buf bytes.Buffer
	_ = joinTmpl.Execute(&buf, tmplData(ref))
	return buf.Bytes()
}
