package api

import "testing"

func TestNormalizeRuntimeHost(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"openclaw/0.0.29", "openclaw/0.0.29", true},
		{"Claude-Code/1.2.3", "claude-code/1.2.3", true},
		{"codex/0.1.4", "codex/0.1.4", true},
		{"terminal", "", false},
		{"attacker/custom", "", false},
		{"codex/<script>", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := normalizeRuntimeHost(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("normalizeRuntimeHost(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}
