package consolev2

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"go.opentelemetry.io/otel/trace"
)

func TestRegistrationPreparationRejectionLogsSafeReason(t *testing.T) {
	const route = "/api/v2/agent-identities/registration-challenges"
	traceID := trace.TraceID{1}
	publicKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, test := range []struct{ reason, body string }{
		{"malformed_json", `{"public_key":`},
		{"invalid_json_type", `{"public_key":123}`},
		{"unknown_json_field", `{"private-test-input":true}`},
		{"public_key_missing", `{}`},
		{"public_key_invalid", `{"public_key":"private-test-input"}`},
		{"idempotency_key_missing", `{"public_key":"` + publicKey + `"}`},
		{"idempotency_key_invalid_length", `{"public_key":"` + publicKey + `","idempotency_key":"short"}`},
	} {
		t.Run(test.reason, func(t *testing.T) {
			var logs bytes.Buffer
			original := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(original) })
			h := server.New(server.WithHostPorts("127.0.0.1:0"))
			h.POST(route, func(ctx context.Context, c *app.RequestContext) {
				ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
					TraceID: traceID, SpanID: trace.SpanID{1},
				}))
				(&Service{}).issuePublicRegistrationChallenge(ctx, c)
			})
			response := ut.PerformRequest(h.Engine, http.MethodPost, route, &ut.Body{
				Body: strings.NewReader(test.body), Len: len(test.body),
			}).Result()
			if response.StatusCode() != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400", response.StatusCode())
			}
			if strings.Contains(logs.String(), "private-test-input") || strings.Contains(logs.String(), publicKey) {
				t.Fatal("request data leaked into the log")
			}
			var event map[string]interface{}
			if err := json.NewDecoder(&logs).Decode(&event); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]interface{}{
				"msg": "Authentication rejected", "traceId": traceID.String(), "stage": "registration",
				"route": route, "code": "INVALID_REQUEST", "status": float64(400), "reason": test.reason,
			} {
				if event[key] != want {
					t.Errorf("%s=%v, want %v", key, event[key], want)
				}
			}
		})
	}
}
