package config

import "testing"

func TestEmailMatchesAnySuffix(t *testing.T) {
	suffixes := []string{"@eftestbot.com"}
	cases := []struct {
		email string
		want  bool
	}{
		{"bot1@eftestbot.com", true},
		{"BOT1@EFTestBot.com", true}, // case-insensitive
		{"  spaced@eftestbot.com  ", true},
		{"real@gmail.com", false},
		{"eftestbot.com@gmail.com", false}, // suffix must be at the end
		{"", false},
	}
	for _, c := range cases {
		if got := EmailMatchesAnySuffix(c.email, suffixes); got != c.want {
			t.Errorf("EmailMatchesAnySuffix(%q) = %v, want %v", c.email, got, c.want)
		}
	}
	if EmailMatchesAnySuffix("x@eftestbot.com", nil) {
		t.Error("no suffixes configured should never match")
	}
}
