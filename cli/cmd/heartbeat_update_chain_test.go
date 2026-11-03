package cmd

import "testing"

func TestAutomaticUpdateContinuationIsBoundedAndForwardOnly(t *testing.T) {
	old := version
	version = "1.0.10"
	t.Cleanup(func() { version = old })
	for _, c := range []struct {
		reexec, from, hops string
		want               bool
	}{
		{"1", "1.0.0", "", true}, {"1", "1.0.5", "0", true}, {"1", "1.0.9", "1", true},
		{"1", "1.0.9", "2", false}, {"1", "1.0.9", "-1", false}, {"1", "1.0.9", "bad", false},
		{"1", "1.0.10", "", false}, {"1", "1.0.11", "", false}, {"1", "invalid", "", false},
		{"", "1.0.0", "", false},
	} {
		t.Run(c.reexec+"/"+c.from+"/"+c.hops, func(t *testing.T) {
			t.Setenv(updateReexecEnv, c.reexec)
			t.Setenv(updateFromVersionEnv, c.from)
			t.Setenv(updateHopsEnv, c.hops)
			if got := continueAutomaticCLIUpdate(); got != c.want {
				t.Fatalf("continuation=%v want %v", got, c.want)
			}
		})
	}
}
