package reqinfo

import (
	"context"
	"testing"
)

func TestRequestStartIsLocalAndPreserved(t *testing.T) {
	if RequestStartedAt(context.Background()) != 0 {
		t.Fatal("background context has a request timestamp")
	}
	first := WithRequestStart(context.Background())
	if RequestStartedAt(first) <= 0 {
		t.Fatal("entry timestamp was not recorded")
	}
	nested := WithRequestStart(context.WithValue(first, "test-only-key", "value"))
	if RequestStartedAt(nested) != RequestStartedAt(first) {
		t.Fatal("nested middleware changed the entry timestamp")
	}
}

func TestSafePluginVersion(t *testing.T) {
	for _, value := range []string{"0.1.8", "1.2.3-rc.1+build"} {
		if SafePluginVersion(value) != value {
			t.Fatalf("valid version rejected: %q", value)
		}
	}
	for _, value := range []string{"", "0.1.8\nextra", "0.1.8/host", "123456789012345678901234567890123"} {
		if SafePluginVersion(value) != "" {
			t.Fatalf("unsafe version accepted")
		}
	}
}
