package middleware

import "testing"

func TestParseVersionNum(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"0.0.3", 3},
		{"0.1.0", 100},
		{"1.0.0", 10000},
		{"1.2.3", 10203},
		{"", 0},
		{"abc", 0},
		{"1.2", 0},
		{"0.100.0", 0},
		{"0.0.100", 0},
		{"0.-1.0", 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseVersionNum(tt.input)
			if got != tt.want {
				t.Fatalf("parseVersionNum(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
