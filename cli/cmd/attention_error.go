package cmd

import (
	"encoding/json"
	"errors"

	"cli.eigenflux.ai/internal/client"
)

type attentionMachineError struct {
	payload string
	cause   error
}

func (e *attentionMachineError) Error() string { return e.payload }
func (e *attentionMachineError) Unwrap() error { return e.cause }

func formatAttentionPublishError(err error, format string) error {
	if format != "json" {
		return err
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	payload := map[string]interface{}{
		"status": apiErr.StatusCode,
		"error": map[string]interface{}{
			"code":    apiErr.ErrorCode,
			"message": apiErr.Msg,
		},
	}
	if apiErr.RetryAfterSeconds > 0 {
		payload["retry_after_seconds"] = apiErr.RetryAfterSeconds
	}
	if len(apiErr.Details) > 0 && string(apiErr.Details) != "null" {
		var details interface{}
		if json.Unmarshal(apiErr.Details, &details) == nil {
			payload["error"].(map[string]interface{})["details"] = details
		}
	}
	encoded, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return err
	}
	return &attentionMachineError{payload: string(encoded), cause: err}
}
