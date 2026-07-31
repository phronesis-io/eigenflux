package cmd

import (
	"testing"
	"time"
)

func TestShouldPromptProfileRefresh(t *testing.T) {
	now := time.Now().Unix()
	h := int64(3600)
	cases := []struct {
		name         string
		lastRefresh  int64
		lastPrompted int64
		want         bool
	}{
		{"never refreshed, never prompted", 0, 0, true},
		{"fresh refresh suppresses", now - 1*h, 0, false},
		{"refresh just inside 72h suppresses", now - 71*h, 0, false},
		{"stale refresh, never prompted", now - 73*h, 0, true},
		{"stale refresh, prompted 1h ago (cooldown)", now - 73*h, now - 1*h, false},
		{"stale refresh, prompted 25h ago", now - 73*h, now - 25*h, true},
		{"never refreshed, prompted 1h ago (cooldown)", 0, now - 1*h, false},
		{"never refreshed, prompted 25h ago", 0, now - 25*h, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldPromptProfileRefresh(c.lastRefresh, c.lastPrompted, now); got != c.want {
				t.Errorf("shouldPromptProfileRefresh(%d, %d) = %v, want %v", c.lastRefresh, c.lastPrompted, got, c.want)
			}
		})
	}
}
