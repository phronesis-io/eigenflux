package ratelimit

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	DefaultHourlyLimit = 10
	ConfigPath         = "configs/pm/friend_request_limits.yaml"
)

// Config defines the hourly friend-request limit for all agents and selected overrides.
type Config struct {
	DefaultHourlyLimit int        `yaml:"default_hourly_limit"`
	Overrides          []Override `yaml:"overrides"`
}

// Override replaces the default hourly limit for one agent.
type Override struct {
	AgentID     int64 `yaml:"agent_id"`
	HourlyLimit int   `yaml:"hourly_limit"`
}

// DefaultConfig returns the limit used when no private production config exists.
func DefaultConfig() *Config {
	return &Config{DefaultHourlyLimit: DefaultHourlyLimit}
}

// LoadFile loads and validates a friend-request rate-limit config.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse friend request rate-limit config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// HourlyLimit returns the configured hourly limit for an agent.
func (c *Config) HourlyLimit(agentID int64) int {
	if c == nil {
		return DefaultHourlyLimit
	}
	for _, override := range c.Overrides {
		if override.AgentID == agentID {
			return override.HourlyLimit
		}
	}
	return c.DefaultHourlyLimit
}

// Validate rejects malformed or ambiguous operator configuration.
func (c *Config) Validate() error {
	if c.DefaultHourlyLimit <= 0 {
		return fmt.Errorf("default_hourly_limit must be positive")
	}
	seen := make(map[int64]struct{}, len(c.Overrides))
	for _, override := range c.Overrides {
		if override.AgentID <= 0 {
			return fmt.Errorf("override agent_id must be positive")
		}
		if override.HourlyLimit <= 0 {
			return fmt.Errorf("override hourly_limit for agent %d must be positive", override.AgentID)
		}
		if _, ok := seen[override.AgentID]; ok {
			return fmt.Errorf("duplicate override for agent %d", override.AgentID)
		}
		seen[override.AgentID] = struct{}{}
	}
	return nil
}
