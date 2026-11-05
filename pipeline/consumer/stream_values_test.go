package consumer

import "testing"

func TestCommissionEnvelopeTextRepresentations(t *testing.T) {
	for _, test := range []struct {
		name   string
		values func() map[string]any
		parse  func(map[string]any) error
	}{
		{"catalogue", func() map[string]any {
			return map[string]any{"event_id": "1", "schema_version": "1", "topic": commissionPublishedTopic, "aggregate_type": "commission", "aggregate_id": "4", "aggregate_version": "2", "occurred_at": "1", "payload_json": "{}"}
		}, func(v map[string]any) error { _, err := parseCommissionEvent(v); return err }},
		{"order", func() map[string]any { return validCommissionNotificationValues(t) }, func(v map[string]any) error { _, _, err := parseCommissionOrderNotification(v); return err }},
		{"moderation", func() map[string]any { return moderationValues(t) }, func(v map[string]any) error { _, err := parseCommissionModerationNotification(v, 2000); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := test.values()
			if err := test.parse(values); err != nil {
				t.Fatalf("string envelope: %v", err)
			}
			for key, value := range values {
				values[key] = []byte(value.(string))
			}
			if err := test.parse(values); err != nil {
				t.Fatalf("byte envelope: %v", err)
			}
			for _, invalid := range []any{nil, 1, 1.0, true, []string{"1"}} {
				values["schema_version"] = invalid
				if err := test.parse(values); err == nil {
					t.Fatalf("accepted non-text field: %T", invalid)
				}
			}
		})
	}
}
