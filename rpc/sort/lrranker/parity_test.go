package lrranker

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// rawVector expands the pre-standardization feature vector using the canonical
// contract (mean=0, scale=1), for comparing against the training-side fixture
// which records raw feature values and the raw-vector hash.
func rawVector(in Input) ([]float64, map[string]float64) {
	raw := buildRaw(in)
	vec := make([]float64, len(canonicalTerms))
	byName := make(map[string]float64, len(canonicalTerms))
	for i, c := range canonicalTerms {
		mt := modelTerm{
			kind: c.kind, source: c.source, transform: c.transform, value: c.value,
			clipMin: c.clipMin, clipMax: c.clipMax, mean: 0, scale: 1,
		}
		v := rawTermValue(&mt, raw)
		vec[i] = v
		byName[c.name] = v
	}
	return vec, byName
}

// TestRawFeatureParity checks Go raw feature construction against the golden
// fixture shared with eigenflux-ml (tests/fixtures/lr_features_v2.json). Named
// expected values are compared within 1e-12; the raw-vector SHA256 is compared
// as a bit-exactness signal but not asserted (cross-language float rounding may
// differ by 1 ULP without affecting scoring within tolerance).
func TestRawFeatureParity(t *testing.T) {
	data, err := os.ReadFile("testdata/lr_features_v2.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture struct {
		FeatureContractVersion string `json:"feature_contract_version"`
		Cases                  []struct {
			Name      string             `json:"name"`
			Row       map[string]any     `json:"row"`
			Expected  map[string]float64 `json:"expected"`
			VectorSHA string             `json:"vector_sha256"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if fixture.FeatureContractVersion != ContractVersion {
		t.Fatalf("fixture contract %q != %q", fixture.FeatureContractVersion, ContractVersion)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}

	for _, c := range fixture.Cases {
		in := DecodeSelfTestInput(c.Row)
		vec, byName := rawVector(in)
		for name, want := range c.Expected {
			got, ok := byName[name]
			if !ok {
				t.Errorf("%s: expected feature %q not present in vector", c.Name, name)
				continue
			}
			if math.Abs(got-want) > 1e-12 {
				t.Errorf("%s: feature %q = %g, want %g", c.Name, name, got, want)
			}
		}
		if h := vectorSHA256(vec); h != c.VectorSHA {
			t.Logf("%s: raw vector sha256 differs (go=%s fixture=%s) — tolerated", c.Name, h, c.VectorSHA)
		} else {
			t.Logf("%s: raw vector sha256 bit-exact match", c.Name)
		}
	}
}

// TestModelSelfTest loads a real Python-trained bundle and runs its embedded
// self_test_cases through the full standardize->logit->sigmoid path, asserting
// parity within 1e-9. This is the end-to-end cross-language scoring guarantee.
func TestModelSelfTest(t *testing.T) {
	sc, err := LoadModel("testdata/model.json")
	if err != nil {
		t.Fatalf("load model: %v", err)
	}
	if len(sc.selfTests) == 0 {
		t.Fatal("bundle has no self_test_cases")
	}
	note, err := sc.selfTest(1e-9)
	if err != nil {
		t.Fatalf("self-test failed: %v", err)
	}
	if note != "" {
		t.Logf("self-test note: %s", note)
	}
	t.Logf("model %s passed %d self-test case(s)", sc.version, len(sc.selfTests))
}
