package need

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

func TestBasicNormalizationWithoutVocabulary(t *testing.T) {
	in, err := Decode([]byte(validInput))
	if err != nil {
		t.Fatal(err)
	}
	in.Target.CandidateNeeds = []string{" Agent  memory ", "Agent memory", "新术语"}
	in.Target.Desc = "  国内可落地的\n低成本 Agent 记忆方案  "
	in.Constraints.Lang = []string{" English ", "en", "zh_CN"}
	in.Constraints.ProviderRegion = []string{"中国", "cn", "US"}
	in.Preferences = " 低成本 "
	before, _ := json.Marshal(in)
	n, err := NormalizeBasic(in)
	if err != nil {
		t.Fatal(err)
	}
	if n.MappingStatus() != MappingUnmapped || len(n.MappedNeeds) != 0 || n.Desc != "国内可落地的 低成本 Agent 记忆方案" {
		t.Fatalf("%+v", n)
	}
	if !reflect.DeepEqual(n.CandidateNeeds, []string{"Agent memory", "新术语"}) {
		t.Fatal(n)
	}
	if !reflect.DeepEqual(n.Constraints.Lang, []string{"en", "zh-CN"}) || !reflect.DeepEqual(n.Constraints.ProviderRegion, []string{"CN", "US"}) {
		t.Fatal(n.Constraints)
	}
	if n.Constraints.BudgetMaxFen != nil || n.Constraints.DeadlineMS != nil {
		t.Fatal("invented hard constraints", n)
	}
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Fatal("mutated source")
	}
	assertNormalizedSchema(t, n)
}

func TestUnknownConstraintAlternativesAreNotNarrowed(t *testing.T) {
	in, _ := Decode([]byte(validInput))
	in.Constraints.Lang = []string{"en", "und-US"}
	in.Constraints.ProviderRegion = []string{"CN", "国内"}
	n, err := NormalizeBasic(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Constraints.Lang) != 0 || len(n.Constraints.ProviderRegion) != 0 {
		t.Fatal("partially resolved alternatives became hard filters", n)
	}
	if !reflect.DeepEqual(n.UnresolvedConstraints.Lang, in.Constraints.Lang) || !reflect.DeepEqual(n.UnresolvedConstraints.ProviderRegion, in.Constraints.ProviderRegion) {
		t.Fatal("lost unresolved source restrictions", n)
	}
	assertNormalizedSchema(t, n)
}

func TestBasicNormalizationRetainsExplicitZeroAndDeadline(t *testing.T) {
	in, _ := Decode([]byte(validInput))
	in.NeedType = "commission"
	zero, deadline := int64(0), int64(1)
	in.Constraints = Constraints{BudgetMaxFen: &zero, Currency: "CNY", MaxDurationMS: &zero, DeadlineMS: &deadline}
	n, err := NormalizeBasic(in)
	if err != nil {
		t.Fatal(err)
	}
	if n.Constraints.BudgetMaxFen == nil || *n.Constraints.BudgetMaxFen != 0 || n.Constraints.Currency != "CNY" || *n.Constraints.DeadlineMS != 1 {
		t.Fatal(n)
	}
	*n.Constraints.BudgetMaxFen = 2
	if *in.Constraints.BudgetMaxFen != 0 {
		t.Fatal("aliased source constraint")
	}
	in.Constraints.Currency = "USD"
	if _, err := NormalizeBasic(in); err == nil {
		t.Fatal("unsupported currency bypassed normalization validation")
	}
	in.Constraints.Currency = "CNY"
	in.NeedType = "broadcast"
	if _, err := NormalizeBasic(in); err == nil {
		t.Fatal("invalid constraints bypassed validation")
	}
}

func TestVocabularyCoverageOnlyAddsSemanticMappings(t *testing.T) {
	in, _ := Decode([]byte(validInput))
	base, _ := NormalizeBasic(in)
	for _, test := range []struct {
		name, want string
		terms      map[string]string
		remaining  int
	}{
		{"empty", MappingUnmapped, map[string]string{}, 2},
		{"unknown", MappingUnmapped, map[string]string{"other": "unrelated"}, 2},
		{"partial", MappingPartial, map[string]string{"数据库": "database"}, 1},
		{"mapped", MappingMapped, map[string]string{"数据库": "database", "Go 教程": "golang.tutorial"}, 0},
		{"many-to-one", MappingMapped, map[string]string{"数据库": "technology", "Go 教程": "technology"}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			n, err := NormalizeWithVocabulary(in, Vocabulary{Version: "taxonomy.v1", Needs: test.terms})
			if err != nil || n.MappingStatus() != test.want || len(n.CandidateNeeds)-len(n.MappedNeeds) != test.remaining {
				t.Fatalf("%+v %v", n, err)
			}
			if n.Desc != base.Desc || !reflect.DeepEqual(n.Constraints, base.Constraints) || !reflect.DeepEqual(n.CandidateNeeds, base.CandidateNeeds) {
				t.Fatal("enrichment changed basic meaning or filters")
			}
			assertNormalizedSchema(t, n)
		})
	}
	for _, v := range []Vocabulary{
		{},
		{Version: "v1", Needs: map[string]string{"数据库": "not a canonical id"}},
		{Version: "v1", Needs: map[string]string{"数据库": "database", " 数据库 ": "other"}},
	} {
		if _, err := NormalizeWithVocabulary(in, v); err == nil {
			t.Fatal("bad vocabulary accepted")
		}
	}
}

func assertNormalizedSchema(t *testing.T, n Normalized) {
	t.Helper()
	schema, err := os.ReadFile("../../contracts/normalized_need.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	result, err := gojsonschema.Validate(gojsonschema.NewBytesLoader(schema), gojsonschema.NewBytesLoader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid() {
		t.Fatal(result.Errors())
	}
}
