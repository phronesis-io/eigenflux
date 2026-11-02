// Package requestcontract defines the shared buyer request document convention.
package requestcontract

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const Path = "inputs/request.txt"

var pathTokens = regexp.MustCompile(`[A-Za-z0-9_./\\-]+`)

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
	for _, token := range pathTokens.FindAllString(text, -1) {
		token = strings.TrimSuffix(token, ".")
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
