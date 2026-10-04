// Package discoverylr scores frozen rule evidence for broadcast recommendations.
package discoverylr

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
)

const Contract = "discovery_lr_v1"

var FeatureNames = []string{"lexical", "semantic", "freshness", "quality", "relevance", "rule_score", "semantic_missing", "origin_baseline", "origin_friend"}

type Result struct {
	ModelVersion string  `json:"model_version"`
	Contract     string  `json:"feature_contract_version"`
	Probability  float64 `json:"probability"`
}
type model struct {
	SchemaVersion   int       `json:"schema_version"`
	ModelType       string    `json:"model_type"`
	Version         string    `json:"model_version"`
	Contract        string    `json:"feature_contract_version"`
	FeatureNames    []string  `json:"feature_names"`
	Coefficients    []float64 `json:"coefficients"`
	Intercept       float64   `json:"intercept"`
	PromotionPassed bool      `json:"promotion_passed"`
	SelfTests       []struct {
		Features    []float64 `json:"features"`
		Probability float64   `json:"probability"`
	} `json:"self_test_cases"`
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func (m *model) score(v []float64) (float64, bool) {
	if len(v) != len(FeatureNames) {
		return 0, false
	}
	z := m.Intercept
	for i, value := range v {
		if !finite(value) || value < 0 || value > 1 {
			return 0, false
		}
		z += value * m.Coefficients[i]
	}
	if !finite(z) {
		return 0, false
	}
	if z >= 0 {
		return 1 / (1 + math.Exp(-z)), true
	}
	exp := math.Exp(z)
	return exp / (1 + exp), true
}

func load(path string) (*model, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("discovery LR bundle too large")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m model
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.SchemaVersion != 1 || m.ModelType != "logistic_regression" || m.Contract != Contract || m.Version == "" || !m.PromotionPassed || !slices.Equal(m.FeatureNames, FeatureNames) || len(m.Coefficients) != len(FeatureNames) || !finite(m.Intercept) || len(m.SelfTests) < 2 {
		return nil, fmt.Errorf("invalid discovery LR contract or promotion state")
	}
	for _, c := range m.Coefficients {
		if !finite(c) {
			return nil, fmt.Errorf("nonfinite discovery LR coefficient")
		}
	}
	for _, test := range m.SelfTests {
		p, ok := m.score(test.Features)
		if !ok || !finite(test.Probability) || math.Abs(p-test.Probability) > 1e-9 {
			return nil, fmt.Errorf("discovery LR self-test failed")
		}
	}
	return &m, nil
}
