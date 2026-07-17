package config

import "testing"

func TestEmailMatchesAnySuffix(t *testing.T) {
	suffixes := []string{"@eftestbot.com", "kairui1@pgc.example.com"}
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
		{"kairui1@pgc.example.com", true},   // full-address entry matches exactly
		{"KaiRui1@pgc.example.com", true},   // exact match is case-insensitive
		{"xkairui1@pgc.example.com", false}, // full-address entry must not match by suffix
		{"kairui2@pgc.example.com", false},  // other addresses on the same domain don't match
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
