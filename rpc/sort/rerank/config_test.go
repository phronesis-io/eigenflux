package rerank

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigBuildsFreshnessPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rerank.yaml")
	err := os.WriteFile(path, []byte(`
policies:
  - name: freshness
    item_rules:
      - broadcast_type: alert
        max_age: 6h
        action: drop
`), 0o644)
	require.NoError(t, err)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	policies, err := cfg.NewPolicies(time.Now)
	require.NoError(t, err)

	require.Len(t, policies, 1)
	policy, ok := policies[0].(*FreshnessPolicy)
	require.True(t, ok)
	require.Len(t, policy.ItemRules, 1)
	assert.Equal(t, "alert", policy.ItemRules[0].BroadcastType)
	assert.Equal(t, 6*time.Hour, policy.ItemRules[0].MaxAge)
	assert.Equal(t, "drop", policy.ItemRules[0].Action)
}

func TestLoadConfigRejectsUnknownPolicy(t *testing.T) {
	cfg := &Config{Policies: []PolicyConfig{{Name: "unknown"}}}
	_, err := cfg.NewPolicies(time.Now)
	require.Error(t, err)
}

func TestLoadConfigBuildsBoostPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rerank.yaml")
	err := os.WriteFile(path, []byte(`
policies:
  - name: boost
    boost_rules:
      - field: type
        values: [supply, demand]
        weight: 1.3
      - field: source_type
        values: [original]
        weight: 1.2
`), 0o644)
	require.NoError(t, err)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	policies, err := cfg.NewPolicies(time.Now)
	require.NoError(t, err)

	require.Len(t, policies, 1)
	policy, ok := policies[0].(*BoostPolicy)
	require.True(t, ok)
	require.Len(t, policy.Rules, 2)
	assert.Equal(t, "type", policy.Rules[0].Field)
	assert.Equal(t, []string{"supply", "demand"}, policy.Rules[0].Values)
	assert.Equal(t, 1.3, policy.Rules[0].Weight)
	assert.Equal(t, "source_type", policy.Rules[1].Field)
	assert.Equal(t, 1.2, policy.Rules[1].Weight)
}

func TestLoadConfigRejectsInvalidBoostRules(t *testing.T) {
	cases := []PolicyConfig{
		{Name: "boost", BoostRules: []BoostRuleConfig{{Field: "bogus", Values: []string{"x"}, Weight: 1.2}}},
		{Name: "boost", BoostRules: []BoostRuleConfig{{Field: "type", Weight: 1.2}}},
		{Name: "boost", BoostRules: []BoostRuleConfig{{Field: "type", Values: []string{"supply"}, Weight: 0}}},
	}
	for _, pc := range cases {
		cfg := &Config{Policies: []PolicyConfig{pc}}
		_, err := cfg.NewPolicies(time.Now)
		require.Error(t, err)
	}
}
