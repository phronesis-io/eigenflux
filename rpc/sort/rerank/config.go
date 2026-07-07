package rerank

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Policies []PolicyConfig `yaml:"policies"`
}

type PolicyConfig struct {
	Name       string                    `yaml:"name"`
	ItemRules  []ItemFreshnessRuleConfig `yaml:"item_rules"`
	BoostRules []BoostRuleConfig         `yaml:"boost_rules"`
}

type ItemFreshnessRuleConfig struct {
	BroadcastType string `yaml:"broadcast_type"`
	MaxAge        string `yaml:"max_age"`
	Action        string `yaml:"action"`
}

type BoostRuleConfig struct {
	Field  string   `yaml:"field"`
	Values []string `yaml:"values"`
	Weight float64  `yaml:"weight"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) NewPolicies(now func() time.Time) ([]Policy, error) {
	if c == nil {
		return nil, nil
	}
	policies := make([]Policy, 0, len(c.Policies))
	for _, pc := range c.Policies {
		switch pc.Name {
		case "freshness":
			policy, err := pc.newFreshnessPolicy(now)
			if err != nil {
				return nil, err
			}
			policies = append(policies, policy)
		case "boost":
			policy, err := pc.newBoostPolicy()
			if err != nil {
				return nil, err
			}
			policies = append(policies, policy)
		default:
			return nil, fmt.Errorf("unknown rerank policy %q", pc.Name)
		}
	}
	return policies, nil
}

func (pc PolicyConfig) newFreshnessPolicy(now func() time.Time) (*FreshnessPolicy, error) {
	rules := make([]ItemFreshnessRule, 0, len(pc.ItemRules))
	for _, rc := range pc.ItemRules {
		maxAge, err := parseConfigDuration(rc.MaxAge)
		if err != nil {
			return nil, fmt.Errorf("freshness rule %q max_age: %w", rc.BroadcastType, err)
		}
		action := rc.Action
		if action == "" {
			action = "drop"
		}
		if action != "drop" {
			return nil, fmt.Errorf("freshness rule %q uses unsupported action %q", rc.BroadcastType, action)
		}
		rules = append(rules, ItemFreshnessRule{
			BroadcastType: rc.BroadcastType,
			MaxAge:        maxAge,
			Action:        action,
		})
	}
	return &FreshnessPolicy{ItemRules: rules, Now: now}, nil
}

func (pc PolicyConfig) newBoostPolicy() (*BoostPolicy, error) {
	rules := make([]BoostRule, 0, len(pc.BoostRules))
	for _, rc := range pc.BoostRules {
		if rc.Field != "type" && rc.Field != "source_type" {
			return nil, fmt.Errorf("boost rule uses unsupported field %q", rc.Field)
		}
		if len(rc.Values) == 0 {
			return nil, fmt.Errorf("boost rule for field %q has no values", rc.Field)
		}
		if rc.Weight <= 0 {
			return nil, fmt.Errorf("boost rule for field %q has non-positive weight %v", rc.Field, rc.Weight)
		}
		rules = append(rules, BoostRule{
			Field:  rc.Field,
			Values: rc.Values,
			Weight: rc.Weight,
		})
	}
	return &BoostPolicy{Rules: rules}, nil
}

func parseConfigDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		for _, ch := range s[:len(s)-1] {
			if ch < '0' || ch > '9' {
				return 0, fmt.Errorf("invalid day duration %q", s)
			}
			days = days*10 + int(ch-'0')
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
