package cmd

import (
	"encoding/json"
	"errors"

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
	post := func(batch []maintenance.Event) (*client.APIResponse, error) {
		return c.PostWithHeaders("/maintenance/events:batch", map[string]interface{}{"events": batch}, headers)
	}
	response, err := post(wire)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode != "INVALID_MAINTENANCE_EVENT" ||
		apiErr.Msg != "uninstall metadata requires uninstall phase" {
		return response, err
	}
	// Older gateways reject the new phase before writing any events. Keep
	// existing maintenance uploads working if the gateway rolls back.
	legacy := wire[:0]
	for _, event := range wire {
		if event.Phase != "pm_owner_notice" {
			legacy = append(legacy, event)
		}
	}
	if len(legacy) == len(events) {
		return response, err
	}
	if len(legacy) == 0 {
		return nil, nil
	}
	return post(legacy)
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
