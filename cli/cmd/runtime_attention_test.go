package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/client"
)

type scriptedRuntimePoster struct {
	errors []error
	bodies [][]byte
}

func (p *scriptedRuntimePoster) Post(_ string, body interface{}) (*client.APIResponse, error) {
	encoded, _ := json.Marshal(body)
	p.bodies = append(p.bodies, encoded)
	index := len(p.bodies) - 1
	if index < len(p.errors) && p.errors[index] != nil {
		return nil, p.errors[index]
	}
	return &client.APIResponse{Code: 0, Data: json.RawMessage(`{"status":"completed"}`)}, nil
}

func TestAttentionRuntimeResultTypedValidationDoesNotChangeGenericCommands(t *testing.T) {
	valid := `{"summary":"已完成处理","related_entities":[{"type":"broadcast","id":"123","url":"/dashboard/broadcast/123"}]}`
	if _, err := parseRuntimeCommandResultForType(valid, attentionResponseCommandType); err != nil {
		t.Fatalf("valid typed result rejected: %v", err)
	}
	invalid := []string{
		`{}`,
		`{"summary":"ok","extra":true}`,
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":"123","url":"https://example.com/123"}]}`,
	}
	for _, raw := range invalid {
		if _, err := parseRuntimeCommandResultForType(raw, attentionResponseCommandType); err == nil {
			t.Errorf("invalid typed result accepted: %s", raw)
		}
	}
	if _, err := parseRuntimeCommandResultForType(`{"arbitrary":true}`, ""); err != nil {
		t.Fatalf("generic command result compatibility changed: %v", err)
	}
	if _, err := parseRuntimeCommandResultForType(valid, "unknown"); err == nil {
		t.Fatal("unknown typed result contract was accepted")
	}
}

func TestRuntimeCompleteRetriesAmbiguousFailureWithIdenticalBody(t *testing.T) {
	poster := &scriptedRuntimePoster{errors: []error{errors.New("connection reset"), &client.APIError{StatusCode: http.StatusServiceUnavailable, Msg: "unavailable"}, nil}}
	request := map[string]interface{}{
		"runtime_instance_id": "runtime-1",
		"claim_epoch":         int64(2),
		"claim_token":         "claim-token",
		"status":              "completed",
		"result":              map[string]interface{}{"summary": "完成"},
	}
	if _, err := postRuntimeCommandComplete(poster, "/agent-commands/1/complete", request, func(time.Duration) {}); err != nil {
		t.Fatalf("bounded retry did not recover: %v", err)
	}
	if len(poster.bodies) != runtimeCommandCompleteMaxAttempts {
		t.Fatalf("attempts=%d, want %d", len(poster.bodies), runtimeCommandCompleteMaxAttempts)
	}
	for index := 1; index < len(poster.bodies); index++ {
		if string(poster.bodies[index]) != string(poster.bodies[0]) {
			t.Fatalf("retry changed frozen completion body: first=%s retry=%s", poster.bodies[0], poster.bodies[index])
		}
	}
}

func TestRuntimeCompleteDoesNotRetryDefinitiveClientError(t *testing.T) {
	poster := &scriptedRuntimePoster{errors: []error{&client.APIError{StatusCode: http.StatusConflict, Msg: "fenced"}}}
	_, err := postRuntimeCommandComplete(poster, "/agent-commands/1/complete", map[string]interface{}{"status": "completed"}, func(time.Duration) {})
	if err == nil || len(poster.bodies) != 1 {
		t.Fatalf("definitive 4xx must not retry: attempts=%d err=%v", len(poster.bodies), err)
	}
}
