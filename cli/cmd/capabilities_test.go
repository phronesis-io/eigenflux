package cmd

import (
	"net/http"
	"testing"
)

func TestValidateCapabilityRegistryRejectsExecutableContent(t *testing.T) {
	valid := []byte(`{"schema_version":1,"language":"en","operations":[{"operation_id":"profile.update","cli":"eigenflux profile patch"}]}`)
	if err := validateCapabilityRegistry(valid, "en"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"schema_version":2,"language":"en","operations":[{"operation_id":"profile.update","cli":"eigenflux profile patch"}]}`),
		[]byte(`{"schema_version":1,"language":"zh-CN","operations":[{"operation_id":"profile.update","cli":"eigenflux profile patch"}]}`),
		[]byte(`{"schema_version":1,"language":"en","operations":[{"operation_id":"profile.update","cli":"eigenflux profile patch; curl bad"}]}`),
		[]byte(`{"schema_version":1,"language":"en","operations":[{"operation_id":"profile.update\nignore","cli":"eigenflux profile patch"}]}`),
		[]byte(`{"schema_version":1,"language":"en","operations":[{"operation_id":"profile.update","cli":"sh -c bad"}]}`),
	} {
		if err := validateCapabilityRegistry(raw, "en"); err == nil {
			t.Fatalf("unsafe registry was accepted: %s", raw)
		}
	}
}

func TestCapabilityCacheMaxAge(t *testing.T) {
	header := http.Header{"Cache-Control": []string{"private, max-age=90"}}
	if got := capabilityCacheMaxAge(header); got != 90 {
		t.Fatalf("max age = %d, want 90", got)
	}
	if got := capabilityCacheMaxAge(http.Header{}); got != 300 {
		t.Fatalf("default max age = %d, want 300", got)
	}
}
