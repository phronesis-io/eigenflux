package lrranker

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"time"
)

// modelTermJSON is one term as serialized in model.json (model.py ModelTerm).
type modelTermJSON struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Source      string   `json:"source"`
	Transform   string   `json:"transform"`
	Value       *string  `json:"value"`
	ClipMin     *float64 `json:"clip_min"`
	ClipMax     *float64 `json:"clip_max"`
	Mean        float64  `json:"mean"`
	Scale       float64  `json:"scale"`
	Coefficient float64  `json:"coefficient"`
}

type selfTestCaseJSON struct {
	Name                 string         `json:"name"`
	Input                map[string]any `json:"input"`
	ExpectedLogit        float64        `json:"expected_logit"`
	ExpectedProbability  float64        `json:"expected_probability"`
	ExpectedVectorSHA256 string         `json:"expected_vector_sha256"`
}

type modelJSON struct {
	SchemaVersion          int                `json:"schema_version"`
	ModelType              string             `json:"model_type"`
	ModelVersion           string             `json:"model_version"`
	FeatureContractVersion string             `json:"feature_contract_version"`
	CreatedAt              string             `json:"created_at"`
	Intercept              float64            `json:"intercept"`
	Terms                  []modelTermJSON    `json:"terms"`
	SelfTestCases          []selfTestCaseJSON `json:"self_test_cases"`
}

// modelTerm is a validated term ready for scoring: contract identity plus the
// trained mean/scale/coefficient.
type modelTerm struct {
	kind      string
	source    string
	transform string
	value     string
	clipMin   float64
	clipMax   float64
	mean      float64
	scale     float64
	coef      float64
}

// LoadModel reads and validates a model.json at path, returning a ready scorer.
// Validation mirrors model.py LRModel.from_dict: schema/type/contract version,
// exact structural term ordering against canonicalTerms, finite numerics and
// positive scales. Self-test cases are NOT run here — the manager runs them via
// scorer.selfTest so a failure can be surfaced as a reload/fallback metric.
func LoadModel(path string) (*scorer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read model: %w", err)
	}
	var m modelJSON
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse model json: %w", err)
	}
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported model schema: %d", m.SchemaVersion)
	}
	if m.ModelType != "logistic_regression" {
		return nil, fmt.Errorf("unsupported model type: %q", m.ModelType)
	}
	if m.FeatureContractVersion != ContractVersion {
		return nil, fmt.Errorf("unsupported feature contract version: %q", m.FeatureContractVersion)
	}
	if len(m.Terms) != len(canonicalTerms) {
		return nil, fmt.Errorf("model has %d terms, expected %d", len(m.Terms), len(canonicalTerms))
	}
	if !isFinite(m.Intercept) {
		return nil, fmt.Errorf("model intercept is not finite")
	}

	terms := make([]modelTerm, len(m.Terms))
	for i, t := range m.Terms {
		want := canonicalTerms[i]
		gotValue := ""
		if t.Value != nil {
			gotValue = *t.Value
		}
		if t.Name != want.name || t.Kind != want.kind || t.Source != want.source ||
			t.Transform != want.transform || gotValue != want.value {
			return nil, fmt.Errorf("model term %d (%q) does not match lr_features_v2 ordering", i, t.Name)
		}
		if !isFinite(t.Mean) || !isFinite(t.Scale) || !isFinite(t.Coefficient) {
			return nil, fmt.Errorf("model term %q has non-finite numeric", t.Name)
		}
		if t.Scale <= 0 {
			return nil, fmt.Errorf("model term %q scale must be positive, got %g", t.Name, t.Scale)
		}
		terms[i] = modelTerm{
			kind:      want.kind,
			source:    want.source,
			transform: want.transform,
			value:     want.value,
			clipMin:   want.clipMin,
			clipMax:   want.clipMax,
			mean:      t.Mean,
			scale:     t.Scale,
			coef:      t.Coefficient,
		}
	}

	return &scorer{
		version:     m.ModelVersion,
		createdAtMS: parseCreatedAtMS(m.CreatedAt),
		intercept:   m.Intercept,
		terms:       terms,
		selfTests:   m.SelfTestCases,
	}, nil
}

func isFinite(f float64) bool { return !math.IsInf(f, 0) && !math.IsNaN(f) }

// parseCreatedAtMS best-effort parses the bundle created_at (RFC3339) for the
// model-age metric; 0 when unparseable (the metric is then simply not emitted).
func parseCreatedAtMS(s string) int64 {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli()
	}
	return 0
}
