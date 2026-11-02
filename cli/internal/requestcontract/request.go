// Package requestcontract defines the shared buyer request document convention.
package requestcontract

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const Path = "inputs/request.txt"

// Quoted paths stay whole, including spaces. Bare declarations are whitespace
// delimited so valid filename characters such as + and Unicode cannot turn a
// different filename into the canonical path.
var pathTokens = regexp.MustCompile("`inputs/[^`]*`|\"inputs/[^\"]*\"|'inputs/[^']*'|[^\\s`\"']+")

// Structured follows the order validator's distinction between {} and a schema
// containing constraints. Empty legacy schemas are equivalent to {}.
func Structured(schema string) (bool, error) {
	if strings.TrimSpace(schema) == "" {
		return false, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(schema), &object); err != nil || object == nil {
		return false, fmt.Errorf("request schema must be a JSON object")
	}
	return len(object) != 0, nil
}

// ValidateSpec binds structured fields to the same path used by upload and
// order validation. Other declared workspace files remain ordinary attachments.
func ValidateSpec(text, schema string) error {
	structured, err := Structured(schema)
	if err != nil || !structured {
		return err
	}
	declared := false
	for _, span := range pathTokens.FindAllStringIndex(text, -1) {
		token := text[span[0]:span[1]]
		if len(token) >= 2 && (token[0] == '`' || token[0] == '"' || token[0] == '\'') && token[len(token)-1] == token[0] {
			if !endsQuotedPath(text[span[1]:]) {
				continue
			}
			token = token[1 : len(token)-1]
		} else {
			// Embedded/unmatched quotes are part of a different filename, not
			// boundaries that can make a bare path declaration canonical.
			if span[0] > 0 && strings.ContainsRune("`\"'", rune(text[span[0]-1])) || span[1] < len(text) && strings.ContainsRune("`\"'", rune(text[span[1]])) {
				continue
			}
			// Sentence punctuation outside a quoted path is prose, not a suffix.
			token = strings.TrimRight(token, ".,;:!?，。；：、！？")
		}
		if token == "inputs/request.json" {
			return fmt.Errorf("structured request specification must use %s, not inputs/request.json", Path)
		}
		declared = declared || token == Path
	}
	if !declared {
		return fmt.Errorf("structured request specification must declare %s containing a UTF-8 JSON object", Path)
	}
	return nil
}

// A closing quote cannot hide a connected filename suffix. Only whitespace,
// sentence punctuation or enclosing prose punctuation may follow it.
func endsQuotedPath(tail string) bool {
	for _, r := range tail {
		if unicode.IsSpace(r) {
			return true
		}
		if strings.ContainsRune("，。；：、！？", r) {
			return true
		}
		if !strings.ContainsRune(".,;:!?，。；：、！？)]}>", r) {
			return false
		}
	}
	return true
}
