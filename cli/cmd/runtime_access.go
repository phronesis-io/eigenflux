package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
)

type runtimeAccess struct {
	Mode            string `json:"mode"`
	OnboardingState string `json:"onboarding_state"`
}

func runtimeAccessForServer(serverName string) (runtimeAccess, error) {
	hasV2, err := auth.HasV2Credentials(serverName)
	if err != nil {
		return runtimeAccess{}, err
	}
	return readRuntimeAccess(newClientForServer(serverName), hasV2)
}

func readRuntimeAccess(c *client.Client, v2 bool) (runtimeAccess, error) {
	if !v2 {
		response, err := c.Get("/agents/me", nil)
		if err != nil {
			return runtimeAccess{}, err
		}
		var data struct {
			Profile map[string]interface{} `json:"profile"`
		}
		if response.Code != 0 || json.Unmarshal(response.Data, &data) != nil || data.Profile == nil {
			return runtimeAccess{}, fmt.Errorf("invalid profile response while resolving legacy runtime access")
		}
		return runtimeAccess{Mode: "legacy", OnboardingState: "completed"}, nil
	}
	response, err := c.Get("/agent-context", nil)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && ((apiErr.StatusCode == http.StatusConflict && apiErr.ErrorCode == "ONBOARDING_REQUIRED") ||
			(apiErr.StatusCode == http.StatusForbidden && apiErr.ErrorCode == "ONBOARDING_INCOMPLETE")) {
			var details struct {
				State string `json:"onboarding_state"`
			}
			_ = json.Unmarshal(apiErr.Details, &details)
			if details.State == "completed" {
				return runtimeAccess{}, fmt.Errorf("inconsistent onboarding restriction: %w", err)
			}
			if details.State == "" {
				details.State = "in_progress"
			}
			return runtimeAccess{Mode: "baseline", OnboardingState: details.State}, nil
		}
		return runtimeAccess{}, err
	}
	var context struct {
		Revision int64 `json:"context_revision"`
	}
	if response.Code != 0 || json.Unmarshal(response.Data, &context) != nil || context.Revision <= 0 {
		return runtimeAccess{}, fmt.Errorf("invalid control-context response while resolving runtime access")
	}
	return runtimeAccess{Mode: "intent_aligned", OnboardingState: "completed"}, nil
}
