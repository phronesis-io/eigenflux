package consumer

import "testing"

func TestSupportedCommunicationActivityTypes(t *testing.T) {
	for _, eventType := range []string{
		"feed_pull", "message_sent", "message_received", "friend_request_sent",
		"friend_request_received", "friend_added",
	} {
		if !isSupportedActivityType(eventType) {
			t.Fatalf("expected %q to be supported", eventType)
		}
	}
}
