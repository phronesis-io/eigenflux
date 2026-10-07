package publishorigin

import (
	"context"
	"testing"
)

func TestNormalizeAcceptsOnlyCanonicalValues(t *testing.T) {
	cases := map[string]string{
		"heartbeat":  Heartbeat,
		"owner":      Owner,
		"":           "",
		"Heartbeat":  "",
		" owner":     "",
		"unknown":    "",
		"scheduled":  "",
		"heartbeat ": "",
	}
	for raw, want := range cases {
		if got := Normalize(raw); got != want {
			t.Fatalf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestContextRoundTripCarriesOnlyRecognizedValues(t *testing.T) {
	for _, origin := range []string{Heartbeat, Owner} {
		if got := FromContext(WithOrigin(context.Background(), origin)); got != origin {
			t.Fatalf("round trip %q = %q", origin, got)
		}
	}
	base := context.Background()
	for _, raw := range []string{"", "bogus", "OWNER"} {
		ctx := WithOrigin(base, raw)
		if ctx != base {
			t.Fatalf("WithOrigin(%q) must leave the context unchanged", raw)
		}
		if got := FromContext(ctx); got != "" {
			t.Fatalf("FromContext after %q = %q, want empty", raw, got)
		}
	}
	if got := FromContext(context.Background()); got != "" {
		t.Fatalf("absent origin = %q, want empty", got)
	}
}
