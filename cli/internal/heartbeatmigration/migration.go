// Package heartbeatmigration validates host-supplied scheduler inventories.
// Only the host's native API can read or mutate the actual scheduler.
package heartbeatmigration

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
)

const Version = 1

type Task struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Prompt   string                 `json:"prompt"`
	Owner    string                 `json:"owner"`
	Home     string                 `json:"home"`
	Server   string                 `json:"server"`
	Schedule string                 `json:"schedule"`
	Status   string                 `json:"status"`
	ThreadID string                 `json:"thread_id"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type Inventory struct {
	Complete bool   `json:"complete"`
	Tasks    []Task `json:"tasks"`
}

type Plan struct {
	Version int       `json:"version"`
	Status  string    `json:"status"`
	TaskID  string    `json:"task_id,omitempty"`
	Before  Inventory `json:"before"`
	After   Inventory `json:"after"`
}

func Build(in Inventory, home, server, launcher string) (Plan, error) {
	p := Plan{Version: Version, Status: "missing", Before: in, After: Inventory{Complete: in.Complete, Tasks: append([]Task(nil), in.Tasks...)}}
	if err := validate(in); err != nil {
		return p, err
	}
	if !filepath.IsAbs(home) || launcher == "" {
		return p, fmt.Errorf("absolute stable Home and launcher required")
	}
	index := -1
	for i, t := range in.Tasks {
		if t.Owner != "eigenflux" {
			continue
		}
		if !filepath.IsAbs(t.Home) || t.Server == "" {
			return p, fmt.Errorf("task %s has ambiguous Home or server", t.ID)
		}
		if filepath.Clean(t.Home) != filepath.Clean(home) || t.Server != server {
			continue
		}
		if index >= 0 {
			return p, fmt.Errorf("multiple EigenFlux tasks match this Home/server; resolve duplicates explicitly")
		}
		index = i
	}
	if index < 0 {
		return p, nil
	}
	t := in.Tasks[index]
	if t.Schedule == "" || t.Status == "" {
		return p, fmt.Errorf("task %s lacks scheduler metadata", t.ID)
	}
	p.TaskID = t.ID
	if strings.TrimSpace(t.Prompt) == launcher {
		p.Status = "current"
		return p, nil
	}
	p.Status = "update"
	p.After.Tasks[index].Prompt = launcher
	return p, nil
}

func Verify(p Plan, actual Inventory) error {
	if p.Version != Version || (p.Status != "update" && p.Status != "current") {
		return fmt.Errorf("no verifiable migration plan")
	}
	if err := validate(actual); err != nil {
		return err
	}
	if len(actual.Tasks) != len(p.After.Tasks) {
		return fmt.Errorf("scheduler task count changed")
	}
	byID := map[string]Task{}
	for _, t := range actual.Tasks {
		byID[t.ID] = t
	}
	for _, t := range p.After.Tasks {
		if got, ok := byID[t.ID]; !ok || !reflect.DeepEqual(got, t) {
			return fmt.Errorf("task %s readback differs from planned prompt or preserved metadata", t.ID)
		}
	}
	return nil
}

func validate(in Inventory) error {
	if !in.Complete {
		return fmt.Errorf("complete host scheduler inventory required")
	}
	seen := map[string]bool{}
	for _, t := range in.Tasks {
		if t.ID == "" || seen[t.ID] {
			return fmt.Errorf("missing or duplicate scheduler task ID")
		}
		seen[t.ID] = true
	}
	return nil
}
