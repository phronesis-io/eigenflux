package need

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

const validInput = `{"schema_version":"need_input.v1","intent_id":"123","intent_version":1,"need_type":"broadcast","target":{"desc":"  请记录：查找 Go 数据库资料。\n保留原文。 ","candidate_needs":["数据库","Go 教程"]}}`

func TestContract(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../contracts/need_input.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gojsonschema.NewSchema(gojsonschema.NewBytesLoader(schemaRaw))
	if err != nil {
		t.Fatal(err)
	}
	add := func(fields string) string { return strings.Replace(validInput, `"target":`, fields+`,"target":`, 1) }
	cases := []struct {
		name, raw string
		valid     bool
	}{
		{"minimal", validInput, true},
		{"agent", strings.Replace(validInput, `"broadcast"`, `"agent"`, 1), true},
		{"commission", strings.Replace(validInput, `"broadcast"`, `"commission"`, 1), true},
		{"noncanonical id", strings.Replace(validInput, `"123"`, `"0123"`, 1), false},
		{"numeric id", strings.Replace(validInput, `"intent_id":"123"`, `"intent_id":123`, 1), false},
		{"zero budget", strings.Replace(add(`"constraints":{"budget_max_fen":0,"currency":"CNY"}`), `"broadcast"`, `"commission"`, 1), true},
		{"USD rejected", strings.Replace(add(`"constraints":{"budget_max_fen":10,"currency":"USD"}`), `"broadcast"`, `"commission"`, 1), false},
		{"EUR rejected", strings.Replace(add(`"constraints":{"currency":"EUR"}`), `"broadcast"`, `"commission"`, 1), false},
		{"empty currency rejected", strings.Replace(add(`"constraints":{"currency":""}`), `"broadcast"`, `"commission"`, 1), false},
		{"lowercase currency rejected", strings.Replace(add(`"constraints":{"currency":"cny"}`), `"broadcast"`, `"commission"`, 1), false},
		{"missing intent link", strings.Replace(validInput, `"intent_id":"123",`, "", 1), false},
		{"bad version", strings.Replace(validInput, `"intent_version":1`, `"intent_version":0`, 1), false},
		{"canonical field", strings.Replace(validInput, `"desc":`, `"category":"design","desc":`, 1), false},
		{"schema version", strings.Replace(validInput, "need_input.v1", "need_input.v2", 1), false},
		{"null priority", add(`"priority":null`), false},
		{"empty candidates", strings.Replace(validInput, `["数据库","Go 教程"]`, `[]`, 1), false},
		{"broadcast budget", add(`"constraints":{"budget_max_fen":10,"currency":"CNY"}`), false},
		{"agent delivery", strings.Replace(add(`"constraints":{"max_promised_delivery_ms":0}`), `"broadcast"`, `"agent"`, 1), false},
		{"budget without currency", strings.Replace(add(`"constraints":{"budget_max_fen":10}`), `"broadcast"`, `"commission"`, 1), false},
		{"priority bounds", add(`"priority":1.1`), false},
		{"constraints object", add(`"constraints":{"lang":["zh"],"exclude_terms":["ads"]}`), true},
		{"string constraints", add(`"constraints":"{}"`), false},
		{"array constraints", add(`"constraints":[]`), false},
		{"removed outcome", add(`"outcome":"obsolete"`), false},
		{"removed exclusions", add(`"constraints":{"exclude_authors":["123"]}`), false},
		{"removed free text", strings.Replace(validInput, `"desc"`, `"free_text"`, 1), false},
		{"removed proposed intents", strings.Replace(validInput, `"candidate_needs"`, `"proposed_intents"`, 1), false},
	}
	for _, oldType := range []string{"find_info", "find_service", "find_people"} {
		cases = append(cases, struct {
			name, raw string
			valid     bool
		}{oldType, strings.Replace(validInput, `"broadcast"`, `"`+oldType+`"`, 1), false})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode([]byte(c.raw))
			if (err == nil) != c.valid {
				t.Fatalf("valid=%v err=%v", c.valid, err)
			}
			result, err := schema.Validate(gojsonschema.NewStringLoader(c.raw))
			if err != nil {
				t.Fatal(err)
			}
			if result.Valid() != c.valid {
				t.Fatalf("schema drift: %v", result.Errors())
			}
		})
	}
	for _, raw := range []string{validInput + `{}`, add(`"intent_version":1`), `null`, strings.Replace(validInput, `"desc":`, `"desc":"duplicate","desc":`, 1)} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	in, err := Decode([]byte(validInput))
	if err != nil || in.Target.Desc != "  请记录：查找 Go 数据库资料。\n保留原文。 " || in.Priority != nil {
		t.Fatalf("changed original: %#v %v", in, err)
	}
	if _, err := Decode([]byte(add(`"constraints":{"deadline_ms":1}`))); err != nil {
		t.Fatal("past deadlines remain source data", err)
	}
}

func TestDescriptionLengthBoundary(t *testing.T) {
	in, err := Decode([]byte(validInput))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		desc  string
		valid bool
	}{
		{strings.Repeat("a", 200), true}, {strings.Repeat("a", 201), false},
		{strings.Repeat("中", 100), true}, {strings.Repeat("中", 101), false},
		{strings.Repeat("中", 99) + "ab", true}, {strings.Repeat("中", 99) + "abc", false},
		{"   ", false}, {"\x00", false},
	} {
		in.Target.Desc = test.desc
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Decode(raw)
		if (err == nil) != test.valid {
			t.Fatalf("description boundary valid=%v err=%v", test.valid, err)
		}
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
	n := Normalized{Desc: "PostgreSQL indexes", CandidateNeeds: []string{"indexes"}, MappedNeeds: map[string]string{"indexes": "database.indexing"}}
	assertNormalizedSchema(t, n)
	encoded, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{`"outcome":"obsolete"`, `"need_type":"broadcast"`, `"priority":0.5`, `"preferences":"unused"`, `"mapping_status":"mapped"`, `"schema_version":"normalized_need.v1"`, `"free_text":"text"`, `"proposed_intents":[]`, `"intents":[]`, `"category":"technology"`} {
		bad := strings.Replace(string(encoded), `"desc":`, obsolete+`,"desc":`, 1)
		result, err := schema.Validate(gojsonschema.NewStringLoader(bad))
		if err != nil {
			t.Fatal(err)
		}
		if result.Valid() {
			t.Fatalf("redundant or removed field accepted: %s", obsolete)
		}
	}
	bad := strings.Replace(string(encoded), `"constraints":{}`, `"constraints":{"exclude_authors":["123"]}`, 1)
	result, err := schema.Validate(gojsonschema.NewStringLoader(bad))
	if err != nil || result.Valid() {
		t.Fatalf("removed constraint accepted: %v", err)
	}
	bad = strings.Replace(string(encoded), `"constraints":{}`, `"constraints":{"budget_max_fen":10,"currency":"USD"}`, 1)
	result, err = schema.Validate(gojsonschema.NewStringLoader(bad))
	if err != nil || result.Valid() {
		t.Fatalf("unsupported normalized currency accepted: %v", err)
	}
}
