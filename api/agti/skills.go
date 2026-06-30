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
	skillTmpl     *template.Template
	joinTmpl      *template.Template
	interpretTmpl *template.Template
	skillBase     string
)

// InitSkills parses the agent-facing skill + join + interpret templates once at
// startup.
func InitSkills(skillPath, joinPath, interpretPath, baseURL string) error {
	st, err := template.ParseFiles(skillPath)
	if err != nil {
		return fmt.Errorf("agti skills template: %w", err)
	}
	jt, err := template.ParseFiles(joinPath)
	if err != nil {
		return fmt.Errorf("agti join template: %w", err)
	}
	it, err := template.ParseFiles(interpretPath)
	if err != nil {
		return fmt.Errorf("agti interpret template: %w", err)
	}
	skillTmpl = st
	joinTmpl = jt
	interpretTmpl = it
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

// renderInterpret renders the post-quiz interpretation brief the agent reads to
// write its principal a personalized read of the result. data is built by the
// handler from the stored result payload (+ BaseUrl/Ref for the join link).
func renderInterpret(data map[string]interface{}) []byte {
	data["BaseUrl"] = skillBase
	var buf bytes.Buffer
	_ = interpretTmpl.Execute(&buf, data)
	return buf.Bytes()
}
