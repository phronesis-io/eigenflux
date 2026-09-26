package need

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

const v2Input = `{"schema_version":"need_input.v2","intent_id":"123","intent_version":3,"need_type":"commission","target":{"goal":"诊断并改善 PostgreSQL 慢查询","context":"先提供诊断报告"},"constraints":{"budget_max_fen":50000,"currency":"CNY","lang":["zh"],"provider_region":["CN"]},"requirements":[{"text":"不得上传生产数据","source_quote":"不要上传生产数据"}],"preferences":[{"text":"中文沟通","source_quote":"最好中文沟通"}]}`

func TestV2ContractAndSchema(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/need_input.v2.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gojsonschema.NewSchema(gojsonschema.NewBytesLoader(raw))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, raw string
		valid     bool
	}{
		{"complete", v2Input, true},
		{"no invented fields", `{"schema_version":"need_input.v2","intent_id":"123","intent_version":1,"need_type":"agent","target":{"goal":"Find a Go code reviewer"}}`, true},
		{"legacy target", strings.Replace(v2Input, `"goal":`, `"desc":`, 1), false},
		{"candidate phrases", strings.Replace(v2Input, `"goal":`, `"candidate_needs":["SQL"],"goal":`, 1), false},
		{"empty requirement", strings.Replace(v2Input, `"text":"不得上传生产数据"`, `"text":""`, 1), false},
		{"preference not string", strings.Replace(v2Input, `[{"text":"中文沟通","source_quote":"最好中文沟通"}]`, `"中文沟通"`, 1), false},
		{"no derived taxonomy", strings.Replace(v2Input, `"target":`, `"taxonomy_version":"v1","target":`, 1), false},
		{"condition null", strings.Replace(v2Input, `"source_quote":"不要上传生产数据"`, `"source_quote":null`, 1), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode([]byte(c.raw))
			if (err == nil) != c.valid {
				t.Fatalf("Decode: %v", err)
			}
			result, err := schema.Validate(gojsonschema.NewStringLoader(c.raw))
			if err != nil || result.Valid() != c.valid {
				t.Fatalf("schema: %v %v", result, err)
			}
		})
	}
	for _, raw := range []string{
		strings.Replace(v2Input, `["zh"]`, `["中文"]`, 1),
		strings.Replace(v2Input, `["CN"]`, `["domestic"]`, 1),
		strings.Replace(v2Input, `["zh"]`, `["en_US"]`, 1),
		strings.Replace(v2Input, `"goal":`, `"goal":"duplicate","goal":`, 1),
		strings.Replace(v2Input, `"text":"不得上传生产数据"`, `"text":"   "`, 1),
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("invalid accepted: %s", raw)
		}
	}
}

func TestV2SourceAndLengthBoundaries(t *testing.T) {
	original, err := Decode([]byte(v2Input))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		edit  func(*Input)
		valid bool
	}{
		{"goal limit", func(in *Input) { in.Target.Goal = strings.Repeat("中", 100) }, true},
		{"goal too long", func(in *Input) { in.Target.Goal = strings.Repeat("中", 101) }, false},
		{"context limit", func(in *Input) { in.Target.Context = strings.Repeat("中", 1000) }, true},
		{"context too long", func(in *Input) { in.Target.Context = strings.Repeat("中", 1001) }, false},
		{"requirements limit", func(in *Input) {
			in.Requirements = make([]Condition, 20)
			for i := range in.Requirements {
				in.Requirements[i] = Condition{Text: "required"}
			}
		}, true},
		{"requirements too many", func(in *Input) { in.Requirements = make([]Condition, 21) }, false},
		{"preference limit", func(in *Input) {
			in.Preferences = []Condition{{Text: strings.Repeat("中", 250), SourceQuote: strings.Repeat("中", 500)}}
		}, true},
		{"preference too long", func(in *Input) { in.Preferences = []Condition{{Text: strings.Repeat("中", 251)}} }, false},
		{"quote too long", func(in *Input) {
			in.Requirements = []Condition{{Text: "required", SourceQuote: strings.Repeat("中", 501)}}
		}, false},
		{"no candidates needed", func(in *Input) { in.Requirements = nil; in.Preferences = nil }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := original
			tc.edit(&in)
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Decode(raw)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	// Compatibility validates the old schema without rewriting source fields.
	legacy, err := decodeCompatible([]byte(validInput))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.IntentID != 123 || legacy.IntentVersion != 1 || legacy.SchemaVersion != "need_input.v1" {
		t.Fatal(legacy)
	}
}
