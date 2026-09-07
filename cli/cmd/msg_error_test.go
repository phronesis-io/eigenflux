package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"cli.eigenflux.ai/internal/client"
)

func TestFormatMessageSendErrorPreservesStructuredDetails(t *testing.T) {
	details := json.RawMessage(`{"scope":"conversation","conv_id":"456","limit":3,"used":3,"remaining":0,"reset_condition":"peer_reply_or_timeout","retry_after_seconds":37}`)
	cause := &client.APIError{
		StatusCode:        http.StatusTooManyRequests,
		Code:              429,
		ErrorCode:         "PM_WAITING_FOR_PEER_REPLY",
		Msg:               "Do not retry immediately; other conversations are unaffected.",
		Details:           details,
		RetryAfterSeconds: 37,
	}
	err := formatMessageSendError(cause, "json")
	if !errors.Is(err, cause) {
		t.Fatal("formatted error did not preserve its cause")
	}
	var payload struct {
		Status            int64 `json:"status"`
		RetryAfterSeconds int64 `json:"retry_after_seconds"`
		Error             struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				ConvID    string `json:"conv_id"`
				Limit     int32  `json:"limit"`
				Remaining int32  `json:"remaining"`
			} `json:"details"`
		} `json:"error"`
	}
	if decodeErr := json.Unmarshal([]byte(err.Error()), &payload); decodeErr != nil {
		t.Fatalf("decode formatted error: %v payload=%s", decodeErr, err)
	}
	if payload.Status != 429 || payload.RetryAfterSeconds != 37 || payload.Error.Code != "PM_WAITING_FOR_PEER_REPLY" {
		t.Fatalf("unexpected formatted error: %#v", payload)
	}
	if payload.Error.Details.ConvID != "456" || payload.Error.Details.Limit != 3 || payload.Error.Details.Remaining != 0 {
		t.Fatalf("details were not preserved: %#v", payload.Error.Details)
	}
	if got := formatMessageSendError(cause, "table"); got != cause {
		t.Fatal("human-readable output should preserve the original API error")
	}
}
