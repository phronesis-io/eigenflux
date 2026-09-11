package publicurl

import (
	"strings"
	"testing"
)

func TestResolveNormalizesConfiguredBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://example.com":                  "https://example.com",
		"https://example.com/":                 "https://example.com",
		"https://example.com/api/v1":           "https://example.com",
		"  https://example.com/root/api/v1/  ": "https://example.com/root",
	}
	for configured, want := range cases {
		if got := Resolve(configured, 8080); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", configured, got, want)
		}
	}
}

func TestResolveFallsBackToLocalHostPort(t *testing.T) {
	for _, configured := range []string{"", "   ", "/"} {
		got := Resolve(configured, 8080)
		if !strings.HasPrefix(got, "http://") || !strings.HasSuffix(got, ":8080") {
			t.Errorf("Resolve(%q) = %q, want http://<host>:8080", configured, got)
		}
	}
}
