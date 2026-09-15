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
	Purpose  string                 `json:"purpose"`
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
	p := Plan{Version: Version, Status: "missing"}
	if err := validate(in); err != nil {
		return p, err
	}
	if !filepath.IsAbs(home) || server == "" || launcher == "" {
		return p, fmt.Errorf("absolute stable Home and launcher required")
	}
	index := -1
	for i, t := range in.Tasks {
		if t.Owner != "eigenflux" || t.Purpose != "heartbeat" {
			continue
		}
		if !filepath.IsAbs(t.Home) || t.Server == "" {
			return p, fmt.Errorf("task %s has ambiguous Home or server", t.ID)
		}
		if filepath.Clean(t.Home) != filepath.Clean(home) || t.Server != server {
			continue
		}
		if index >= 0 {
			return p, fmt.Errorf("multiple EigenFlux heartbeat tasks match this Home/server; resolve duplicates explicitly")
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
	p.Before = Inventory{Complete: true, Tasks: []Task{t}}
	p.After = Inventory{Complete: true, Tasks: []Task{t}}
	if strings.TrimSpace(t.Prompt) == launcher {
		p.Status = "current"
		return p, nil
	}
	p.Status = "update"
	p.After.Tasks[0].Prompt = launcher
	return p, nil
}

func Verify(p Plan, actual Inventory) error {
	if p.Version != Version || (p.Status != "update" && p.Status != "current") {
		return fmt.Errorf("no verifiable migration plan")
	}
	if err := validate(actual); err != nil {
		return err
	}
	if len(p.After.Tasks) != 1 || p.After.Tasks[0].ID != p.TaskID {
		return fmt.Errorf("plan must contain exactly one target heartbeat")
	}
	want := p.After.Tasks[0]
	if want.Owner != "eigenflux" || want.Purpose != "heartbeat" {
		return fmt.Errorf("plan target is not an EigenFlux heartbeat")
	}
	fresh, err := Build(actual, want.Home, want.Server, want.Prompt)
	if err != nil {
		return err
	}
	if fresh.TaskID != p.TaskID || len(fresh.Before.Tasks) != 1 {
		return fmt.Errorf("target heartbeat missing or replaced")
	}
	got := fresh.Before.Tasks[0]
	if len(got.Metadata) == 0 {
		got.Metadata = nil
	}
	if len(want.Metadata) == 0 {
		want.Metadata = nil
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("task %s readback differs from planned prompt or preserved metadata", want.ID)
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
