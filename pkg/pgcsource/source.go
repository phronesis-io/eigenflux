// Package pgcsource separates source identity from the reader-facing article.
package pgcsource

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tidwall/sjson"
)

const titleLabel = "Source title: "
const timeLabel = "Source timestamp (UTC): "

type Fields struct {
	Title       string `json:"title"`
	Timestamp   string `json:"timestamp"`
	ModelPrefix string `json:"model_prefix"`
}

func validTitle(s string) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsAny(s, "\r\n") && utf8.RuneCountInString(s) <= 240
}
func validTime(s string) bool {
	t, err := time.Parse(time.RFC3339, s)
	return err == nil && t.UTC().Format("2006-01-02T15:04:05+00:00") == s
}

func FromNotes(notes string) (Fields, bool) {
	var n struct {
		Source Fields `json:"pgc_source"`
	}
	if !strings.Contains(notes, `"pgc_source"`) || json.Unmarshal([]byte(notes), &n) != nil {
		return Fields{}, false
	}
	f := n.Source
	if !validTitle(f.Title) || (f.Timestamp != "" && !validTime(f.Timestamp)) {
		return Fields{}, false
	}
	if f.ModelPrefix != "" && f.ModelPrefix != titleLabel+f.Title && (f.Timestamp == "" || (f.ModelPrefix != timeLabel+f.Timestamp && f.ModelPrefix != titleLabel+f.Title+"\n"+timeLabel+f.Timestamp)) {
		return Fields{}, false
	}
	return f, true
}

// ModelInput restores the publisher's bounded legacy prefix in memory only.
func (f Fields) ModelInput(body, notes string) (string, string) {
	if f.ModelPrefix != "" {
		body = f.ModelPrefix + "\n\n" + body
	}
	clean, _ := sjson.Delete(notes, "pgc_source")
	return body, clean
}

// Read is called only for verified PGC publishers. Legacy prefixes have one
// or two lines followed by a blank line and a nonempty article.
func Read(content, notes string) (string, Fields) {
	if f, ok := FromNotes(notes); ok {
		return content, f
	}
	head, body, ok := strings.Cut(content, "\n\n")
	if !ok || strings.TrimSpace(body) == "" {
		return content, Fields{}
	}
	f := Fields{}
	lines := strings.Split(head, "\n")
	if title, ok := strings.CutPrefix(lines[0], titleLabel); ok && validTitle(title) {
		f.Title = title
		lines = lines[1:]
	}
	if len(lines) == 1 {
		if stamp, ok := strings.CutPrefix(lines[0], timeLabel); ok && validTime(stamp) {
			f.Timestamp = stamp
			lines = lines[1:]
		}
	}
	if len(lines) != 0 {
		return content, Fields{}
	}
	return body, f
}
