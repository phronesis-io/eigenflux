package main

import (
	"testing"
	"time"
)

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

func TestHomepageEligibilityRetryAtDefersFailedItem(t *testing.T) {
	now := time.UnixMilli(1_000)
	want := now.Add(time.Hour).UnixMilli()
	if got := homepageEligibilityRetryAt(now); got != want {
		t.Fatalf("retry at = %d, want %d", got, want)
	}
}
