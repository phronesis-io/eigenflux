package push

import "testing"

func TestFriendResponseEnvelopePreservesPublicIdentity(t *testing.T) {
	for _, eventType := range []string{"friend_accepted", "friend_rejected"} {
		t.Run(eventType, func(t *testing.T) {
			envelope, ok := friendResponseEnvelope(`{"type":"` + eventType + `","friend_uid":"123","peer_short_id":"AbCdE","peer_display_name":"Atlas"}`)
			if !ok {
				t.Fatal("expected structured friend response")
			}
			if envelope.Type != eventType {
				t.Fatalf("type=%q, want %q", envelope.Type, eventType)
			}
			data, ok := envelope.Data.(map[string]string)
			if !ok {
				t.Fatalf("unexpected data type %T", envelope.Data)
			}
			if data["friend_uid"] != "123" || data["peer_short_id"] != "AbCdE" || data["peer_display_name"] != "Atlas" {
				t.Fatalf("unexpected data: %#v", data)
			}
		})
	}
}

func TestFriendResponseEnvelopeRejectsIncompleteOrUnknownPayload(t *testing.T) {
	for _, payload := range []string{
		`not-json`,
		`{"type":"friend_accepted","friend_uid":"123","peer_short_id":"AbCdE"}`,
		`{"type":"friend_request","friend_uid":"123","peer_short_id":"AbCdE","peer_display_name":"Atlas"}`,
	} {
		if _, ok := friendResponseEnvelope(payload); ok {
			t.Fatalf("unexpected match for %q", payload)
		}
	}
}
