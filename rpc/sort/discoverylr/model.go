// Package discoverylr scores frozen evidence for broadcast recommendations.
package discoverylr

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
)

// Contract predicts an exact-impression follow-up from rule evidence.
const Contract = "discovery_lr_v1"

// RatingContract predicts a positive rating (score >= 1 among rated
// deliveries) from rule evidence, item attributes, recall attribution and
// Need/profile similarity.
const RatingContract = "discovery_rating_lr_v1"

var FeatureNames = []string{"lexical", "semantic", "freshness", "quality", "relevance", "rule_score", "semantic_missing", "origin_baseline", "origin_friend"}

// RatingFeatureNames extends the rule inputs with bounded attributes; every
// input is in [0,1]. The definitions are in docs/dev/discovery.md.
var RatingFeatureNames = []string{
	"f_lexical", "f_semantic", "f_fresh", "f_quality", "relevance", "value", "sem_missing", "o_baseline", "o_friend",
	"o_need", "o_profile", "ct_info", "ct_demand", "ct_supply", "ct_alert", "is_pgc", "known_contact",
	"ch_dense", "ch_friend", "ch_hot_recall", "ch_lexical", "ch_lexical_recent", "ch_new_recall", "ch_new_ugc_recall", "ch_swing_i2i",
	"src_curated", "src_original", "src_forwarded", "ttl01",
	"need_cos_ctx0", "need_cos_max0", "prof_cos0", "cos_best0", "has_need", "has_prof",
	"need_cos_max_h35", "need_cos_max_h45", "prof_cos_h35", "prof_cos_h45", "n_needs01",
}

func featureNames(contract string) ([]string, bool) {
	switch contract {
	case Contract:
		return FeatureNames, true
	case RatingContract:
		return RatingFeatureNames, true
	}
	return nil, false
}

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
	if len(v) != len(m.FeatureNames) {
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

func load(path, contract string) (*model, error) {
	names, ok := featureNames(contract)
	if !ok {
		return nil, fmt.Errorf("unsupported discovery LR contract")
	}
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
	if m.SchemaVersion != 1 || m.ModelType != "logistic_regression" || m.Contract != contract || m.Version == "" || !m.PromotionPassed || !slices.Equal(m.FeatureNames, names) || len(m.Coefficients) != len(names) || !finite(m.Intercept) || len(m.SelfTests) < 2 {
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
