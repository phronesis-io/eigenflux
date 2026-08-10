package api

import "testing"

func TestConsoleBroadcastLimit(t *testing.T) {
	tests := []struct {
		raw  string
		want int
	}{
		{raw: "", want: 100},
		{raw: "invalid", want: 100},
		{raw: "0", want: 1},
		{raw: "10", want: 10},
		{raw: "101", want: 100},
	}
	for _, tt := range tests {
		if got := consoleBroadcastLimit(tt.raw); got != tt.want {
			t.Errorf("consoleBroadcastLimit(%q)=%d, want %d", tt.raw, got, tt.want)
		}
	}
}
