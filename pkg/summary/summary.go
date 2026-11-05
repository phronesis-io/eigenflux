// Package summary defines publisher ownership and display provenance.
package summary

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const MaxRunes = 280
const TranslationVersion = "literal-zh-v1"

// Publisher reads optional notes.summary without rewriting the decoded text.
func Publisher(notes string) (string, error) {
	if strings.TrimSpace(notes) == "" {
		return "", nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(notes), &obj); err != nil {
		return "", fmt.Errorf("notes must be a JSON object; correct notes or remove it")
	}
	raw, ok := obj["summary"]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("notes.summary must be a string or null; correct it or remove summary")
	}
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxRunes {
		return "", fmt.Errorf("notes.summary must not exceed %d Unicode characters; shorten it or remove summary", MaxRunes)
	}
	return s, nil
}
func Origin(s string) string {
	switch s {
	case "publisher", "platform", "none":
		return s
	}
	return "unknown"
}

// TranslationKey is a cache identity, not a security digest.
func TranslationKey(source string) string {
	h := md5.Sum([]byte(source))
	return TranslationVersion + ":" + hex.EncodeToString(h[:])
}

type Preview struct {
	Text          string `json:"text"`
	Source        string `json:"source"`
	SummaryOrigin string `json:"summary_origin"`
	Truncated     bool   `json:"truncated"`
}

func MakePreview(text, body, origin string, limit int) Preview {
	p := Preview{Text: text, Source: "summary", SummaryOrigin: Origin(origin)}
	if strings.TrimSpace(text) == "" {
		p.Text = body
		p.Source = "content"
		p.SummaryOrigin = "none"
	}
	runes := []rune(p.Text)
	if limit >= 0 && len(runes) > limit {
		p.Text = string(runes[:limit])
		p.Truncated = true
	}
	return p
}
