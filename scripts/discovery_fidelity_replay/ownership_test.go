package main

import "testing"

func TestOwnershipCounterfactualKeepsPGCAndRejectsInvalidUGC(t *testing.T) {
	for _, tc := range []struct {
		class, notes, want string
		drop               bool
	}{
		{"pgc", `{"summary":123}`, "original", false},
		{"ugc", `{"summary":"  FTC  "}`, "  FTC  ", false},
		{"ugc", `{}`, "", false},
		{"ugc", `{"summary":null}`, "", false},
		{"ugc", `{"summary":123}`, "", true},
	} {
		got, drop := ownershipProjection(itemEvidence{Notes: tc.notes, Summary: "original"}, tc.class)
		if got != tc.want || drop != tc.drop {
			t.Fatalf("%+v: got %q,%v", tc, got, drop)
		}
	}
}
