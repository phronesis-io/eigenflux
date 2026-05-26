package metrics

import "testing"

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/api/v1/item/12345", "/api/v1/item/:id"},
		{"/api/v1/feed", "/api/v1/feed"},
		{"/api/v1/profile/99/items", "/api/v1/profile/:id/items"},
		{"/health", "/health"},
		{"/api/v1/item/12345/stats", "/api/v1/item/:id/stats"},
	}
	for _, tt := range tests {
		got := normalizePath(tt.input)
		if got != tt.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
