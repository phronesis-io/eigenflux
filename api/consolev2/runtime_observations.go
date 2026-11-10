package consolev2

import (
	"fmt"
	"regexp"
)

// Runtime observations never contain device names, user content or hardware identifiers.
type runtimeObservation struct {
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

func runtimeFailureCode(code string) bool {
	switch code {
	case "authentication_failed", "permission_denied", "timeout", "network_error", "rate_limited", "server_error", "host_unavailable", "unknown_failure":
		return true
	}
	return false
}

func validateRuntimeObservation(e maintenanceEvent) error {
	r := e.Runtime
	if r == nil || e.Component != "runtime" || (e.Trigger != "auto" && e.Trigger != "manual") || (e.Mode != "" && e.Mode != "skill" && e.Mode != "plugin") {
		return fmt.Errorf("invalid runtime observation")
	}
	if e.OuterLoop != nil || e.Commission != nil || e.Notification != nil || e.Source != "" || e.Platform != "" || e.ReasonCategory != "" || e.ToVersion != "" || e.FromVersion != "" || e.RunningVersion != "" || e.DurationMS != 0 {
		return fmt.Errorf("unexpected runtime metadata")
	}
	if e.ErrorCode != "" && (e.Phase != "cycle" || e.Result != "failed" || r.Stage == "cycle" || !runtimeFailureCode(e.ErrorCode)) {
		return fmt.Errorf("error code requires a confirmed stage failure category")
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
	if e.Phase == "lifecycle" {
		if r.PlanMode != "" {
			return fmt.Errorf("lifecycle cannot claim a plan mode")
		}
		valid := false
		switch r.Stage {
		case "trigger":
			valid = e.Result == "observed"
		case "permission":
			switch e.Result {
			case "allowed", "legacy_mismatch", "prompt", "forbidden", "unknown", "required", "denied":
				valid = true
			}
		case "guard":
			valid = e.Result == "configured" || e.Result == "active"
		case "execution":
			valid = e.Result == "interrupted"
		}
		if !valid {
			return fmt.Errorf("invalid lifecycle stage or result")
		}
		return nil
	}
	if e.Phase != "cycle" || (r.PlanMode != "full" && r.PlanMode != "maintenance" && r.PlanMode != "control" && r.PlanMode != "baseline") {
		return fmt.Errorf("invalid cycle mode")
	}
	if r.Stage == "" {
		return fmt.Errorf("cycle stage required")
	}
	switch e.Result {
	case "plan_created", "incomplete", "permission_required", "permission_denied", "interrupted":
		if r.Stage != "cycle" {
			return fmt.Errorf("cycle observation requires cycle stage")
		}
		return nil
	}
	if e.Result != "started" && e.Result != "completed" && e.Result != "failed" {
		return fmt.Errorf("invalid cycle result")
	}
	return nil
}
