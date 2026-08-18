package agentcard

import (
	"fmt"
	"unicode/utf8"
)

// ConsoleV2FieldLimits are additive product limits for the new console only.
// The legacy registry remains unchanged so V1 clients keep their old contract.
var ConsoleV2FieldLimits = map[string]int{
	"agent_name":         40,
	"agent_description":  500,
	"human_description":  500,
	"working_languages":  100,
	"seeking":            1000,
	"offering":           1000,
	"agent_status":       1000,
	"human_status":       1000,
	"interests_negative": 500,
}

func ValidateConsoleV2Value(field string, value interface{}) error {
	limit, constrained := ConsoleV2FieldLimits[field]
	if !constrained {
		return nil
	}
	total := 0
	switch typed := value.(type) {
	case string:
		total = utf8.RuneCountInString(typed)
	case []string:
		for _, item := range typed {
			total += utf8.RuneCountInString(item)
		}
	case []interface{}:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return fmt.Errorf("%s must contain strings", field)
			}
			total += utf8.RuneCountInString(text)
		}
	default:
		return fmt.Errorf("%s has an unsupported value", field)
	}
	if total > limit {
		return fmt.Errorf("%s exceeds the Console V2 limit of %d characters", field, limit)
	}
	return nil
}
