package schemaval

import (
	"strings"
	"testing"
)

func TestValidate_ValidDocument(t *testing.T) {
	schema := `{"type":"object","properties":{"task":{"type":"string"}},"required":["task"]}`
	doc := `{"task":"translate doc"}`
	if err := Validate(schema, doc); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidate_MissingRequiredField(t *testing.T) {
	schema := `{"type":"object","properties":{"task":{"type":"string"}},"required":["task"]}`
	doc := `{"other":"value"}`
	err := Validate(schema, doc)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Fatalf("expected error to mention validation failure, got %v", err)
	}
}

func TestValidate_TypeMismatch(t *testing.T) {
	schema := `{"type":"object","properties":{"count":{"type":"integer"}}}`
	doc := `{"count":"not an integer"}`
	if err := Validate(schema, doc); err == nil {
		t.Fatal("expected type-mismatch error, got nil")
	}
}

func TestValidate_MalformedSchema(t *testing.T) {
	schema := `{not valid json`
	doc := `{"task":"x"}`
	if err := Validate(schema, doc); err == nil {
		t.Fatal("expected error for malformed schema, got nil")
	}
}

func TestValidate_MalformedDocument(t *testing.T) {
	schema := `{"type":"object"}`
	doc := `{not valid json`
	if err := Validate(schema, doc); err == nil {
		t.Fatal("expected error for malformed document, got nil")
	}
}

func TestValidate_EmptySchemaAcceptsAny(t *testing.T) {
	// An empty schema (no constraints) accepts any JSON value.
	if err := Validate(`{}`, `{"anything":true}`); err != nil {
		t.Fatalf("expected nil error for empty schema, got %v", err)
	}
}

func TestValidate_NestedObjectConstraint(t *testing.T) {
	schema := `{
		"type":"object",
		"properties":{
			"user":{
				"type":"object",
				"properties":{
					"id":{"type":"integer"}
				},
				"required":["id"]
			}
		},
		"required":["user"]
	}`

	if err := Validate(schema, `{"user":{"id":42}}`); err != nil {
		t.Fatalf("valid nested doc rejected: %v", err)
	}
	if err := Validate(schema, `{"user":{}}`); err == nil {
		t.Fatal("nested missing-field doc should fail validation")
	}
}
