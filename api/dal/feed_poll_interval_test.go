package dal

import "testing"

// TestFeedPollIntervalInRange guards the bounds enforced on every write path so
// no endpoint can persist an out-of-range poll cadence (e.g. 0 -> poll hammer).
func TestFeedPollIntervalInRange(t *testing.T) {
	tests := []struct {
		v    int32
		want bool
	}{
		{0, false},
		{-1, false},
		{9, false},
		{10, true},
		{300, true},
		{3600, true},
		{86400, true},
		{86401, false},
		{2000000000, false},
	}
	for _, tt := range tests {
		if got := FeedPollIntervalInRange(tt.v); got != tt.want {
			t.Errorf("FeedPollIntervalInRange(%d) = %v, want %v", tt.v, got, tt.want)
		}
	}
}
