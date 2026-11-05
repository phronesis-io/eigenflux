package cmd

import "testing"

func TestCLIVersionOutdated(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"1.0.13", "1.0.10", false}, {"1.0.13", "1.0.13", false},
		{"1.0.13", "1.0.14", true}, {"1.0.9", "1.0.13", true},
		{"1.9.0", "1.10.0", true}, {"dev", "1.0.13", false},
		{"1.0.13", "", false}, {"1.0.13", "invalid", false},
	} {
		t.Run(tc.current+"/"+tc.latest, func(t *testing.T) {
			if got := cliVersionOutdated(tc.current, tc.latest); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
