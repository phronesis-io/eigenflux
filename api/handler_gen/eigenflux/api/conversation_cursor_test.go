package api

import "testing"

func TestConversationPageCursorRoundTrip(t *testing.T) {
	token := encodeConversationPageCursor(1756961234567, 318621003607441408)
	cursor, err := decodeConversationPageCursor(token)
	if err != nil {
		t.Fatalf("decodeConversationPageCursor() error = %v", err)
	}
	if cursor.UpdatedAt != 1756961234567 || cursor.ConvID != 318621003607441408 {
		t.Fatalf("decoded cursor = %+v", cursor)
	}
}

func TestConversationPageCursorAcceptsLegacyTimestamp(t *testing.T) {
	cursor, err := decodeConversationPageCursor("1756961234567")
	if err != nil {
		t.Fatalf("decodeConversationPageCursor() error = %v", err)
	}
	if cursor.UpdatedAt != 1756961234567 || cursor.ConvID != 0 {
		t.Fatalf("decoded legacy cursor = %+v", cursor)
	}
}

func TestConversationPageCursorRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"0", "-1", "not-a-cursor", "eyJ1IjoxLCJjIjowfQ"} {
		if _, err := decodeConversationPageCursor(raw); err == nil {
			t.Fatalf("decodeConversationPageCursor(%q) succeeded", raw)
		}
	}
}
