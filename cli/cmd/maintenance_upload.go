package cmd

import (
	"encoding/json"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/maintenance"
)

const watchFailuresHeader = "X-EigenFlux-Watch-Failures"

// Keep the original wire schema so an older gateway can accept the full batch.
// Detail stays in the local journal and travels as optional request metadata.
func postMaintenanceEvents(c *client.Client, events []maintenance.Event) (*client.APIResponse, error) {
	wire := append([]maintenance.Event(nil), events...)
	reasons := make(map[string]string)
	for i := range wire {
		if wire[i].OuterLoop == nil || wire[i].OuterLoop.FailureReason == "" {
			continue
		}
		reasons[wire[i].EventID] = wire[i].OuterLoop.FailureReason
		outer := *wire[i].OuterLoop
		outer.FailureReason = ""
		wire[i].OuterLoop = &outer
	}
	headers := make(map[string]string)
	if len(reasons) > 0 {
		raw, err := json.Marshal(reasons)
		if err == nil && len(raw) <= 8192 && len(reasons) <= 50 {
			headers[watchFailuresHeader] = string(raw)
		}
	}
	return c.PostWithHeaders("/maintenance/events:batch", map[string]interface{}{"events": wire}, headers)
}
