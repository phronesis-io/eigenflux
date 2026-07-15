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
	Name        string                    `yaml:"name"`
	ItemRules   []ItemFreshnessRuleConfig `yaml:"item_rules"`
	BoostRules  []BoostRuleConfig         `yaml:"boost_rules"`
	InjectRules []InjectRuleConfig        `yaml:"inject_rules"`
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

// InjectRuleConfig declares a force-insertion rule: pull up to Count candidates
// recalled from the named source (matched against recallsource.Names labels)
// into Positions. Empty Positions front-fills. The rule carries only
// parameters; the runtime InjectPolicy predicate is built by the sort handler,
// which owns the per-request recall-source map.
//
// ClaimTTL (a Go duration string, e.g. "90m") throttles re-insertion: after an
// item is force-inserted and delivered, the handler marks it in Redis for this
// long so subsequent feeds skip it. Because the offline recall index refreshes
// only periodically, a just-exposed item lingers in the index until the next
// refresh; the claim bridges that lag so each item is force-inserted ~once
// instead of into every feed across the whole refresh window. Empty disables
// the claim (no throttle).
type InjectRuleConfig struct {
	Source    string `yaml:"source"`
	Count     int    `yaml:"count"`
	Positions []int  `yaml:"positions"`
	ClaimTTL  string `yaml:"claim_ttl"`
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
		case "inject":
			// Inject rules carry only parameters; the runnable InjectPolicy needs
			// the per-request recall-source map, so it is built by the handler
			// (see Config.InjectRules) rather than added to the generic chain.
			if err := pc.validateInjectRules(); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unknown rerank policy %q", pc.Name)
		}
	}
	return policies, nil
}

// InjectRules returns every inject_rule declared under any policy named
// "inject", flattened in declaration order.
func (c *Config) InjectRules() []InjectRuleConfig {
	if c == nil {
		return nil
	}
	var rules []InjectRuleConfig
	for _, pc := range c.Policies {
		if pc.Name == "inject" {
			rules = append(rules, pc.InjectRules...)
		}
	}
	return rules
}

// ParsedClaimTTL parses ClaimTTL into a duration. Empty → 0 (no claim). Reuses
// the same duration grammar as the rest of the rerank config (supports a "d"
// day suffix).
func (r InjectRuleConfig) ParsedClaimTTL() (time.Duration, error) {
	return parseConfigDuration(r.ClaimTTL)
}

func (pc PolicyConfig) validateInjectRules() error {
	for _, rc := range pc.InjectRules {
		if rc.Source == "" {
			return fmt.Errorf("inject rule has empty source")
		}
		if rc.Count <= 0 {
			return fmt.Errorf("inject rule for source %q has non-positive count %d", rc.Source, rc.Count)
		}
		for _, p := range rc.Positions {
			if p < 0 {
				return fmt.Errorf("inject rule for source %q has negative position %d", rc.Source, p)
			}
		}
		if rc.ClaimTTL != "" {
			if _, err := parseConfigDuration(rc.ClaimTTL); err != nil {
				return fmt.Errorf("inject rule for source %q has invalid claim_ttl %q: %w", rc.Source, rc.ClaimTTL, err)
			}
		}
	}
	return nil
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
		if rc.Field != "type" && rc.Field != "source_type" && rc.Field != "content_class" {
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
