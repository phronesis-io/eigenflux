package api

import "testing"

// TestFeedPollRampForCreatedAt covers the onboarding-ramp boundary: new agents
// poll at 3600s for their first 3 days, then 300s, with 300s as the fallback
// when registration time is unknown.
func TestFeedPollRampForCreatedAt(t *testing.T) {
	const nowMs int64 = 1_000_000_000_000
	day := int64(24 * 60 * 60 * 1000)

	tests := []struct {
		name      string
		createdAt int64
		want      int32
	}{
		{"unknown created_at", 0, feedPollRampSteadySec},
		{"negative created_at", -5, feedPollRampSteadySec},
		{"just registered", nowMs, feedPollRampNewSec},
		{"1 day old", nowMs - day, feedPollRampNewSec},
		{"just under 3 days", nowMs - (3*day - 1), feedPollRampNewSec},
		{"exactly 3 days", nowMs - 3*day, feedPollRampSteadySec},
		{"5 days old", nowMs - 5*day, feedPollRampSteadySec},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := feedPollRampForCreatedAt(tt.createdAt, nowMs); got != tt.want {
				t.Fatalf("feedPollRampForCreatedAt(%d, %d) = %d, want %d", tt.createdAt, nowMs, got, tt.want)
			}
		})
	}
}
