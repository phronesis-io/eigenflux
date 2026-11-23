package discoverylr

import "fmt"

const SnapshotContract = "discovery_snapshot_v1"

var SnapshotFeatureNames = []string{
	"lexical", "semantic", "freshness", "quality", "relevance", "rule_score", "semantic_missing",
	"query_present", "query_length", "age_hours", "ttl_hours", "has_expiry", "language_match", "language_constrained",
	"origin_query", "origin_need_input", "origin_agent_context", "origin_baseline", "origin_friend", "origin_unknown",
	"content_type_info", "content_type_alert", "content_type_supply", "content_type_demand", "content_type_unknown",
	"source_type_original", "source_type_curated", "source_type_forwarded", "source_type_unknown",
}

// Trees use the numerical split and leaf arrays from the Python exporter.
type tree struct {
	Feature   []int     `json:"feature"`
	Threshold []float64 `json:"threshold"`
	Left      []int     `json:"left"`
	Right     []int     `json:"right"`
	Value     []float64 `json:"value"`
}

func (t *tree) validate(dim int) error {
	n := len(t.Feature)
	if n == 0 || len(t.Threshold) != n || len(t.Left) != n || len(t.Right) != n || len(t.Value) != n {
		return fmt.Errorf("invalid GBDT node arrays")
	}
	seen := make([]bool, n)
	stack := []int{0}
	visited := 0
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if i < 0 || i >= n || seen[i] {
			return fmt.Errorf("invalid GBDT child index or repeated node %d", i)
		}
		seen[i] = true
		visited++
		if !finite(t.Threshold[i]) || !finite(t.Value[i]) {
			return fmt.Errorf("nonfinite GBDT node %d", i)
		}
		if t.Feature[i] == -1 {
			if t.Left[i] != -1 || t.Right[i] != -1 {
				return fmt.Errorf("invalid GBDT leaf %d", i)
			}
			continue
		}
		if t.Feature[i] < 0 || t.Feature[i] >= dim {
			return fmt.Errorf("invalid GBDT feature at node %d", i)
		}
		stack = append(stack, t.Left[i], t.Right[i])
	}
	if visited != n {
		return fmt.Errorf("unreachable GBDT nodes")
	}
	return nil
}

func (t *tree) predict(v []float64) float64 {
	i := 0
	for t.Feature[i] != -1 {
		if v[t.Feature[i]] <= t.Threshold[i] {
			i = t.Left[i]
		} else {
			i = t.Right[i]
		}
	}
	return t.Value[i]
}
