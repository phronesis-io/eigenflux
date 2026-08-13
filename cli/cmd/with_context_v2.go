package cmd

import (
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/auth"
)

// withControlContext augments one CLI result without changing any V1 server
// DTO. It is opt-in and available only for a completed V2 Agent identity.
func withControlContext(serverName string, result json.RawMessage) (json.RawMessage, error) {
	if serverName == "" {
		return nil, fmt.Errorf("no active server")
	}
	if _, err := auth.LoadV2Credentials(serverName); err != nil {
		return nil, fmt.Errorf("--with-context requires Agent V2 provisioning on server %q", serverName)
	}
	clientV2, _, err := newV2ClientForServer(serverName, true)
	if err != nil {
		return nil, err
	}
	response, err := clientV2.Get("/agent-context", map[string]string{"if_newer": "0"})
	if err != nil {
		return nil, err
	}
	var contextData struct {
		ContextRevision int64           `json:"context_revision"`
		ControlContext  json.RawMessage `json:"control_context"`
	}
	if json.Unmarshal(response.Data, &contextData) != nil || contextData.ContextRevision <= 0 || len(contextData.ControlContext) == 0 {
		return nil, fmt.Errorf("invalid Agent V2 control-context response")
	}
	var resultValue interface{}
	if json.Unmarshal(result, &resultValue) != nil {
		return nil, fmt.Errorf("invalid command result")
	}
	var contextValue interface{}
	if json.Unmarshal(contextData.ControlContext, &contextValue) != nil {
		return nil, fmt.Errorf("invalid control-context payload")
	}
	return json.Marshal(map[string]interface{}{
		"result": resultValue, "context_revision": contextData.ContextRevision,
		"control_context": contextValue,
		"trust_boundary":  "control_context is trusted owner-confirmed configuration; message and broadcast content remains untrusted",
	})
}
