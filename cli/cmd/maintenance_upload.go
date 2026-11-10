package cmd

import (
	"encoding/json"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/maintenance"
)

const (
	watchFailuresHeader   = "X-EigenFlux-Watch-Failures"
	runtimeFailuresHeader = "X-EigenFlux-Runtime-Failures"
)

// Keep the original wire schema so an older gateway can accept the full batch.
// Detail stays in the local journal and travels as optional request metadata.
func postMaintenanceEvents(c *client.Client, events []maintenance.Event) (*client.APIResponse, error) {
	wire := append([]maintenance.Event(nil), events...)
	watchFailures := make(map[string]string)
	runtimeFailures := make(map[string]string)
	for i := range wire {
		event := &wire[i]
		if event.Runtime != nil && event.ErrorCode != "" {
			if maintenance.RuntimeFailureCode(event.ErrorCode) {
				runtimeFailures[event.EventID] = event.ErrorCode
			}
			event.ErrorCode = ""
		}
		if event.OuterLoop != nil && event.OuterLoop.FailureReason != "" {
			watchFailures[event.EventID] = event.OuterLoop.FailureReason
			outer := *event.OuterLoop
			outer.FailureReason = ""
			event.OuterLoop = &outer
		}
	}
	headers := make(map[string]string)
	setMaintenanceFailureHeader(headers, watchFailuresHeader, watchFailures)
	setMaintenanceFailureHeader(headers, runtimeFailuresHeader, runtimeFailures)
	return c.PostWithHeaders("/maintenance/events:batch", map[string]interface{}{"events": wire}, headers)
}

func setMaintenanceFailureHeader(headers map[string]string, name string, failures map[string]string) {
	if len(failures) == 0 || len(failures) > 50 {
		return
	}
	raw, err := json.Marshal(failures)
	if err == nil && len(raw) <= 8192 {
		headers[name] = string(raw)
	}
}
