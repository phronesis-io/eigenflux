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
      - field: content_class
        values: [ugc]
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
	assert.Equal(t, "content_class", policy.Rules[1].Field)
	assert.Equal(t, []string{"ugc"}, policy.Rules[1].Values)
	assert.Equal(t, 1.2, policy.Rules[1].Weight)
}

func TestLoadConfigParsesInjectRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rerank.yaml")
	err := os.WriteFile(path, []byte(`
policies:
  - name: inject
    inject_rules:
      - source: new_ugc_recall
        count: 1
        positions: []
        claim_ttl: 90m
`), 0o644)
	require.NoError(t, err)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	// Inject rules are not added to the generic policy chain.
	policies, err := cfg.NewPolicies(time.Now)
	require.NoError(t, err)
	assert.Empty(t, policies)

	rules := cfg.InjectRules()
	require.Len(t, rules, 1)
	assert.Equal(t, "new_ugc_recall", rules[0].Source)
	assert.Equal(t, 1, rules[0].Count)
	assert.Empty(t, rules[0].Positions)
	ttl, err := rules[0].ParsedClaimTTL()
	require.NoError(t, err)
	assert.Equal(t, 90*time.Minute, ttl)
}

func TestParsedClaimTTL_EmptyIsZero(t *testing.T) {
	ttl, err := InjectRuleConfig{}.ParsedClaimTTL()
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), ttl)
}

func TestLoadConfigRejectsInvalidInjectRules(t *testing.T) {
	cases := []PolicyConfig{
		{Name: "inject", InjectRules: []InjectRuleConfig{{Source: "", Count: 1}}},
		{Name: "inject", InjectRules: []InjectRuleConfig{{Source: "new_ugc_recall", Count: 0}}},
		{Name: "inject", InjectRules: []InjectRuleConfig{{Source: "new_ugc_recall", Count: 1, Positions: []int{-1}}}},
		{Name: "inject", InjectRules: []InjectRuleConfig{{Source: "new_ugc_recall", Count: 1, ClaimTTL: "bogus"}}},
	}
	for _, pc := range cases {
		cfg := &Config{Policies: []PolicyConfig{pc}}
		_, err := cfg.NewPolicies(time.Now)
		require.Error(t, err)
	}
}

func TestRealRerankYAMLLoads(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "..", "configs", "sort", "rerank.yaml"))
	require.NoError(t, err)
	_, err = cfg.NewPolicies(time.Now)
	require.NoError(t, err)
	rules := cfg.InjectRules()
	require.Len(t, rules, 1)
	assert.Equal(t, "new_ugc_recall", rules[0].Source)
	assert.Equal(t, 1, rules[0].Count)
	assert.Empty(t, rules[0].Positions)
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
