package maintenance

import (
	"fmt"
	"regexp"
)

// Runtime observations never contain device names, user content or hardware identifiers.
type RuntimeObservation struct {
	Mode           string `json:"mode,omitempty"`
	Product        string `json:"product,omitempty"`
	ProductVersion string `json:"product_version,omitempty"`
	OS             string `json:"os,omitempty"`
	Arch           string `json:"arch,omitempty"`
	Model          string `json:"model,omitempty"`
	CLI            string `json:"cli,omitempty"`
	Skills         string `json:"skills,omitempty"`
	Stage          string `json:"stage,omitempty"`
	PlanMode       string `json:"plan_mode,omitempty"`
}

var runtimeToken = regexp.MustCompile(`^[A-Za-z0-9_.:/+@-]{0,128}$`)

func ValidateRuntime(e Event) error {
	r := e.Runtime
	if r == nil || e.Component != "runtime" || (e.Trigger != "auto" && e.Trigger != "manual") || (e.Mode != "" && e.Mode != "skill" && e.Mode != "plugin") {
		return fmt.Errorf("invalid runtime observation")
	}
	if e.Commission != nil || e.Notification != nil || e.Source != "" || e.Platform != "" || e.ReasonCategory != "" || e.ToVersion != "" || e.FromVersion != "" || e.RunningVersion != "" || e.ErrorCode != "" || e.DurationMS != 0 {
		return fmt.Errorf("unexpected runtime metadata")
	}
	for _, v := range []string{e.Host, r.Mode, r.Product, r.ProductVersion, r.OS, r.Arch, r.Model, r.CLI, r.Skills, r.Stage, r.PlanMode} {
		if !runtimeToken.MatchString(v) {
			return fmt.Errorf("invalid runtime token")
		}
	}
	if e.Phase == "environment" {
		if e.Result != "observed" || r.Stage != "" || r.PlanMode != "" {
			return fmt.Errorf("invalid environment observation")
		}
		return nil
	}
	if e.Phase != "cycle" || (r.PlanMode != "full" && r.PlanMode != "maintenance" && r.PlanMode != "control" && r.PlanMode != "baseline") {
		return fmt.Errorf("invalid cycle mode")
	}
	if r.Stage == "" {
		return fmt.Errorf("cycle stage required")
	}
	if e.Result != "started" && e.Result != "completed" && e.Result != "failed" {
		return fmt.Errorf("invalid cycle result")
	}
	return nil
}
