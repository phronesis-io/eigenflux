// Package featureindex owns the registered online feature views, Redis access
// and bounded periodic materialization. Domain adapters own source reads.
package featureindex

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	Broadcast            = "broadcast.item"
	Agent                = "agent.card"
	Commission           = "commission.catalogue"
	CommissionStatistics = "commission.statistics"
)

// Definition is the complete persisted field allowlist for one versioned view.
// Fields outside this view (including embeddings) are never materialized.
// A zero TTL means event/periodic refresh; no missing field receives a default.
type Definition struct {
	Name         string        `yaml:"name"`
	Entity       string        `yaml:"entity"`
	Component    string        `yaml:"component"`
	IDField      string        `yaml:"id_field"`
	VersionField string        `yaml:"version_field"`
	Fields       []string      `yaml:"fields"`
	TTL          time.Duration `yaml:"ttl"`
	RetentionTTL time.Duration `yaml:"retention_ttl"`
	Load         *LoadConfig   `yaml:"load,omitempty"`
	plans        *sync.Map
}

// Definitions returns copies of the current YAML snapshot for registration audits.
func Definitions() []Definition { return defaultRegistry.Definitions() }

func (r *Registry) resolve(namespace, component string) (Definition, error) {
	entity, generation, ok := strings.Cut(namespace, ":")
	if !ok || generation == "" {
		return Definition{}, fmt.Errorf("feature generation is required")
	}
	for _, d := range r.current.Load().definitions {
		if d.Entity == entity && d.Component == component {
			return d, nil
		}
	}
	return Definition{}, fmt.Errorf("unregistered feature view %s.%s", entity, component)
}

func (d Definition) project(id, version int64, value any) ([]byte, error) {
	input, err := d.selectFields(value)
	if err != nil {
		return nil, err
	}

	for field, expected := range map[string]int64{d.IDField: id, d.VersionField: version} {
		n, err := strconv.ParseInt(string(input[field]), 10, 64)
		if err != nil || n != expected {
			return nil, fmt.Errorf("%s: invalid %s", d.Name, field)
		}
	}
	return json.Marshal(input)
}

// LoadConfig controls page pacing separately from a pause between completed scans.
type LoadConfig struct {
	Interval   time.Duration `yaml:"interval"`
	Timeout    time.Duration `yaml:"timeout"`
	BatchSize  int           `yaml:"batch_size"`
	CyclePause time.Duration `yaml:"cycle_pause"`
}
