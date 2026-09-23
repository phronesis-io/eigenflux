package need

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

const validInput = `{"schema_version":"need_input.v1","intent_id":"123","intent_version":1,"need_type":"find_info","target":{"free_text":"  请记录：查找 Go 数据库资料。\n保留原文。 ","proposed_intents":["数据库","Go 教程"]},"outcome":"学习索引"}`

func TestContract(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../contracts/need_input.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gojsonschema.NewSchema(gojsonschema.NewBytesLoader(schemaRaw))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, raw string
		valid     bool
	}{
		{"minimal", validInput, true},
		{"noncanonical id", strings.Replace(validInput, `"123"`, `"0123"`, 1), false},
		{"numeric id", strings.Replace(validInput, `"intent_id":"123"`, `"intent_id":123`, 1), false},
		{"service zero budget", strings.Replace(validInput, `"find_info"`, `"find_service","constraints":{"budget_max_fen":0,"currency":"CNY"}`, 1), true},
		{"missing intent link", strings.Replace(validInput, `"intent_id":"123",`, "", 1), false},
		{"stale format", strings.Replace(validInput, `"intent_version":1`, `"intent_version":0`, 1), false},
		{"canonical field", strings.Replace(validInput, `"free_text":`, `"category":"design","free_text":`, 1), false},
		{"version", strings.Replace(validInput, "need_input.v1", "need_input.v2", 1), false},
		{"null target", strings.Replace(validInput, `"outcome":`, `"priority":null,"outcome":`, 1), false},
		{"missing outcome", strings.Replace(validInput, `,"outcome":"学习索引"`, "", 1), false},
		{"empty intents", strings.Replace(validInput, `["数据库","Go 教程"]`, `[]`, 1), false},
		{"wrong kind budget", strings.Replace(validInput, `"outcome":`, `"constraints":{"budget_max_fen":10,"currency":"CNY"},"outcome":`, 1), false},
		{"budget without currency", strings.Replace(strings.Replace(validInput, `"find_info"`, `"find_service"`, 1), `"outcome":`, `"constraints":{"budget_max_fen":10},"outcome":`, 1), false},
		{"priority bounds", strings.Replace(validInput, `"outcome":`, `"priority":1.1,"outcome":`, 1), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode([]byte(c.raw))
			if (err == nil) != c.valid {
				t.Fatalf("valid=%v err=%v", c.valid, err)
			}
			result, err := schema.Validate(gojsonschema.NewStringLoader(c.raw))
			if err != nil || result.Valid() != c.valid {
				t.Fatalf("schema drift: %v %v", err, result.Errors())
			}
		})
	}
	for _, raw := range []string{validInput + `{}`, strings.Replace(validInput, `"outcome":`, `"outcome":"duplicate","outcome":`, 1), strings.Replace(validInput, "学习索引", strings.Repeat("中", 251), 1), strings.Replace(validInput, "学习索引", `\u0000`, 1), `null`, strings.Replace(validInput, `"学习索引"`, `"   "`, 1)} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid input")
		}
	}
	in, err := Decode([]byte(validInput))
	if err != nil || in.Target.FreeText != "  请记录：查找 Go 数据库资料。\n保留原文。 " || in.Priority != nil {
		t.Fatalf("changed original: %#v %v", in, err)
	}
	var shape map[string]any
	_ = json.Unmarshal([]byte(validInput), &shape)
	shape["constraints"] = map[string]any{"deadline_ms": float64(1)}
	raw, _ := json.Marshal(shape)
	if _, err := Decode(raw); err != nil {
		t.Fatal("capture preserves deadlines; eligibility is evaluated by downstream consumers", err)
	}
}

func TestNormalizedProjectionSchema(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/normalized_need.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gojsonschema.NewSchema(gojsonschema.NewBytesLoader(raw))
	if err != nil {
		t.Fatal(err)
	}
	projection := Normalized{SchemaVersion: NormalizedSchemaVersion, NeedType: "find_info", QueryText: "PostgreSQL indexes", Outcome: "Learn indexing", Category: "technology", Intents: []string{"database.indexing"}, IntentPhrases: []string{"indexes"}, UnmappedIntents: []string{}, MappingStatus: MappingMapped}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	result, err := schema.Validate(gojsonschema.NewBytesLoader(encoded))
	if err != nil || !result.Valid() {
		t.Fatalf("projection schema mismatch: %v %v", err, result.Errors())
	}
	for _, bad := range []string{
		strings.Replace(string(encoded), `"normalized_need.v1"`, `"need_input.v1"`, 1),
		strings.Replace(string(encoded), `"constraints":{}`, `"constraints":{"budget_max_fen":10,"currency":"USD"}`, 1),
	} {
		result, err := schema.Validate(gojsonschema.NewStringLoader(bad))
		if err != nil || result.Valid() {
			t.Fatalf("invalid projection accepted: %s %v", bad, err)
		}
	}
}
