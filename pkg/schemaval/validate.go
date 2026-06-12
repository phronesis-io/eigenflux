package schemaval

import (
	"fmt"

	"github.com/xeipuuv/gojsonschema"
)

// Validate validates a JSON document against a JSON Schema.
// Returns nil if valid. Returns an error with validation details if invalid.
func Validate(schema, document string) error {
	schemaLoader := gojsonschema.NewStringLoader(schema)
	docLoader := gojsonschema.NewStringLoader(document)

	result, err := gojsonschema.Validate(schemaLoader, docLoader)
	if err != nil {
		return err
	}

	if !result.Valid() {
		errs := result.Errors()
		if len(errs) > 0 {
			return fmt.Errorf("validation failed: %s", errs[0].String())
		}
	}
	return nil
}
