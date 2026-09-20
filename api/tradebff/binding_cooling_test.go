package tradebff

import (
	"encoding/json"
	"testing"
)

func TestNormalizeBindingPreservesOptionalCoolingPolicy(t *testing.T) {
	for _, input := range []string{
		`{"cooling_until":2000,"cooling_applies":false}`,
		`{"cooling_until":2000,"cooling_applies":true}`,
		`{"cooling_until":2000}`,
	} {
		var binding map[string]interface{}
		if err := json.Unmarshal([]byte(input), &binding); err != nil {
			t.Fatal(err)
		}
		want, expected := binding["cooling_applies"]
		result := normalizeBinding(binding).(map[string]interface{})
		got, present := result["cooling_applies"]
		if got != want || present != expected {
			t.Fatalf("cooling policy changed: %s -> %#v", input, result)
		}
		if result["cooling_until"] != "1970-01-01T00:00:02Z" {
			t.Fatalf("deadline changed: %#v", result)
		}
	}
}
