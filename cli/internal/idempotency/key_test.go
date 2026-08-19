package idempotency

import (
	"strings"
	"testing"
)

func TestKeyIsStableAndScoped(t *testing.T) {
	one, err := Key("", "agent-1", "order.create", map[string]any{"commission_id": "2"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := Key("", "agent-1", "order.create", map[string]any{"commission_id": "2"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := Key("", "agent-2", "order.create", map[string]any{"commission_id": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if one != two || one == other {
		t.Fatalf("unexpected keys: %q %q %q", one, two, other)
	}
}

func TestKeyPreservesExplicitValue(t *testing.T) {
	got, err := Key(" retry-key ", "scope", "op", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "retry-key" {
		t.Fatalf("key = %q", got)
	}
}

func TestKeyRejectsExplicitValueBeyondServiceLimit(t *testing.T) {
	if _, err := Key(strings.Repeat("x", 65), "scope", "op", nil); err == nil {
		t.Fatal("expected key longer than 64 bytes to fail")
	}
}
