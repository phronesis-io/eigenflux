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
