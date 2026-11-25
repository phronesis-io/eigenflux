package consolev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"go.opentelemetry.io/otel/trace"
)

func TestOnboardingDraftIntentPriority(t *testing.T) {
	for _, tt := range []struct {
		name    string
		field   string
		want    int16
		wantErr bool
	}{
		{name: "omitted"},
		{name: "null", field: `"priority":null`},
		{name: "integer", field: `"priority":10`, want: 10},
		{name: "integer string", field: `"priority":"10"`, want: 10},
		{name: "negative integer string", field: `"priority":"-32768"`, want: -32768},
		{name: "high label", field: `"priority":" HIGH "`, want: 10},
		{name: "medium label", field: `"priority":"medium"`, want: 5},
		{name: "normal label", field: `"priority":"normal"`, want: 5},
		{name: "low label", field: `"priority":" LOW "`},
		{name: "unknown label", field: `"priority":"urgent"`, wantErr: true},
		{name: "fraction", field: `"priority":"1.5"`, wantErr: true},
		{name: "overflow", field: `"priority":"32768"`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"intent_actions":[{` + tt.field + `}]}`
			var payload draftPayload
			err := json.Unmarshal([]byte(raw), &payload)
			if (err != nil) != tt.wantErr {
				t.Fatalf("decode error = %v, want error %t", err, tt.wantErr)
			}
			if !tt.wantErr && int16(payload.IntentActions[0].Priority) != tt.want {
				t.Fatalf("priority = %d, want %d", payload.IntentActions[0].Priority, tt.want)
			}
		})
	}
}

func TestDecodeOnboardingDraftStepChecksOnlyCurrentSection(t *testing.T) {
	draft := []byte(`{
		"identity_card":{"agent_name":"Agent"},
		"network_goal":"Find infrastructure updates",
		"intent_actions":[{"priority":"urgent"}],
		"security_boundary":{"auto_reply_pm":"false"}
	}`)
	for _, step := range []int16{2, 3} {
		payload, err := decodeOnboardingDraftStep(draft, step)
		if err != nil {
			t.Fatalf("step %d was blocked by another section: %v", step, err)
		}
		if err := validateDraftStep(payload, step); err != nil {
			t.Fatalf("step %d validation failed: %v", step, err)
		}
	}
	for _, step := range []int16{4, 5} {
		if _, err := decodeOnboardingDraftStep(draft, step); err == nil {
			t.Fatalf("step %d accepted an invalid field in its own section", step)
		}
	}
}

func TestOnboardingDraftDecodeLogFields(t *testing.T) {
	for _, tt := range []struct {
		name, priority string
		want           []interface{}
	}{
		{"unsupported label", `"低"`, []interface{}{"reason", "invalid_integer_string", "field", "intent_actions.priority", "expected_type", "int16", "received_type", "string", "received_label", "低"}},
		{"arbitrary text", `"private draft text"`, []interface{}{"reason", "invalid_integer_string", "field", "intent_actions.priority", "expected_type", "int16", "received_type", "string"}},
		{"string out of range", `"32768"`, []interface{}{"reason", "integer_out_of_range", "field", "intent_actions.priority", "expected_type", "int16", "received_type", "string"}},
		{"numeric literal omitted", `123456789`, []interface{}{"reason", "type_mismatch", "field", "priority", "expected_type", "int16", "received_type", "number"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			draft := []byte(`{"intent_actions":[{"priority":` + tt.priority + `}]}`)
			_, err := decodeOnboardingDraftStep(draft, 4)
			if err == nil {
				t.Fatal("expected priority decoding to fail")
			}
			if got := onboardingDraftDecodeLogFields(err); !slices.Equal(got, tt.want) {
				t.Fatalf("log fields = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOnboardingDraftWriteRejectionLogsOnlySafeMetadata(t *testing.T) {
	for _, tt := range []struct {
		intents string
		field   string
		reason  string
	}{
		{`{}`, "intent_actions", "invalid_list_type"},
		{`[{}, "draft-private-value"]`, "intent_actions[1]", "invalid_item_type"},
		{`[{}, {"priority":1.5}]`, "intent_actions[1].priority", "fractional_number"},
		{`[{}, {"priority":"32768"}]`, "intent_actions[1].priority", "integer_out_of_range"},
		{`[{}, {"priority":-32769}]`, "intent_actions[1].priority", "integer_out_of_range"},
		{`[{}, {"priority":"draft-private-value"}]`, "intent_actions[1].priority", "invalid_integer_string"},
		{`[{}, {"priority":true}]`, "intent_actions[1].priority", "type_mismatch"},
	} {
		t.Run(tt.reason, func(t *testing.T) {
			raw := json.RawMessage(`{"intent_actions":` + tt.intents + `}`)
			_, draft, err := normalizeOnboardingDraftJSON(raw)
			if err != nil {
				t.Fatal(err)
			}
			var validation *onboardingDraftWriteError
			if err := normalizeDraftIntentPriorities(draft); !errors.As(err, &validation) ||
				validation.field != tt.field || validation.reason != tt.reason {
				t.Fatalf("unexpected validation result: %#v", validation)
			}
		})
	}

	const traceID = "0123456789abcdef0123456789abcdef"
	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.PUT("/draft", func(ctx context.Context, c *app.RequestContext) {
		ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: tid, SpanID: trace.SpanID{1},
		}))
		c.Set("agent_id", int64(1))
		c.Request.Header.Set("Cookie", "secret-test-session")
		(&Service{}).putOnboardingDraft(ctx, c)
	})
	status, payload, _ := performJSON(t, h, "PUT", "/draft", putDraftRequest{
		ExpectedRevision: 1,
		IdempotencyKey:   "test-key",
		Draft:            json.RawMessage(`{"intent_actions":[{},{"priority":"draft-private-value"}]}`),
	})
	if status != http.StatusBadRequest || responseErrorCode(t, payload) != "INVALID_DRAFT" {
		t.Fatalf("unexpected response status=%d payload=%v", status, payload)
	}
	logLine := output.String()
	if strings.Contains(logLine, "draft-private-value") || strings.Contains(logLine, "secret-test-session") {
		t.Fatal("draft or session data leaked into logs")
	}
	var event map[string]interface{}
	if err := json.NewDecoder(&output).Decode(&event); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]interface{}{
		"msg": "onboarding_draft_rejected", "status": float64(400), "code": "INVALID_DRAFT",
		"stage": "agent_save", "field": "intent_actions[1].priority", "reason": "invalid_integer_string", "traceId": traceID,
	} {
		if event[field] != want {
			t.Errorf("%s=%v, want %v", field, event[field], want)
		}
	}
}
