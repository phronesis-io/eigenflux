package main

import "testing"

func TestHomepageReasonForDistributionDiscard(t *testing.T) {
	for input, want := range map[string]string{
		"self_log": "internal_log", "spam": "advertising", "gibberish": "low_substance",
		"paywall": "low_substance", "malicious": "other",
	} {
		if got := homepageReasonForDistributionDiscard(input); got != want {
			t.Fatalf("reason(%q) = %q, want %q", input, got, want)
		}
	}
}
