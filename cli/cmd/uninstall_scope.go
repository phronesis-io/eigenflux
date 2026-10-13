package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
)

type uninstallScopePlan struct {
	Selected      []installationHome
	Remaining     installationRecord
	SharedSkills  map[string]bool
	RetainedPaths []string
}

// Agent ownership is the canonical registered Home, independent of host aliases.
func selectUninstallScope(record installationRecord, home, scope string) (uninstallScopePlan, error) {
	plan := uninstallScopePlan{Remaining: record, SharedSkills: map[string]bool{}}
	if scope != "current" {
		plan.Selected = record.Homes
		return plan, nil
	}
	canonical, err := filepath.EvalSymlinks(home)
	if err != nil {
		return plan, err
	}
	plan.Remaining.Homes = nil
	for _, entry := range record.Homes {
		if entry.Home == canonical {
			plan.Selected = append(plan.Selected, entry)
		} else {
			plan.Remaining.Homes = append(plan.Remaining.Homes, entry)
			plan.RetainedPaths = append(plan.RetainedPaths, entry.Home)
			if entry.SkillsTarget != "" {
				plan.SharedSkills[entry.SkillsTarget] = true
				plan.RetainedPaths = append(plan.RetainedPaths, entry.SkillsTarget)
			}
		}
	}
	for _, selected := range plan.Selected {
		if selected.SkillsTarget == "" {
			continue
		}
		// Registrations do not cover older Agents. Only a target inside this
		// Home has enough ownership evidence for current-Agent removal.
		if !strings.HasPrefix(selected.SkillsTarget, canonical+string(filepath.Separator)) {
			plan.SharedSkills[selected.SkillsTarget] = true
		}
		for _, remaining := range plan.Remaining.Homes {
			for _, path := range []string{remaining.Home, remaining.SkillsTarget} {
				if path != "" && (path == selected.SkillsTarget || strings.HasPrefix(path, selected.SkillsTarget+string(filepath.Separator)) || strings.HasPrefix(selected.SkillsTarget, path+string(filepath.Separator))) {
					plan.SharedSkills[selected.SkillsTarget] = true
				}
			}
		}
	}
	for path, shared := range plan.SharedSkills {
		if shared {
			plan.RetainedPaths = append(plan.RetainedPaths, path)
		}
	}
	if len(plan.Selected) == 0 {
		return plan, fmt.Errorf("current Agent Home is not registered in this installation")
	}
	return plan, nil
}
