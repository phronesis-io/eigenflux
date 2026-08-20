package config

import (
	"fmt"
	"testing"
)

var controlledPGCTestPatterns = []string{
	"kairui[0-9]@pgc.eigenflux.one",
	"kairui[1-9][0-9]@pgc.eigenflux.one",
	"lingan[0-9]@pgc.eigenflux.one",
	"lingan[1-9][0-9]@pgc.eigenflux.one",
	"weici[0-9]@pgc.eigenflux.one",
	"weici[1-9][0-9]@pgc.eigenflux.one",
	"vic[0-9]@pgc.eigenflux.one",
	"vic[1-9][0-9]@pgc.eigenflux.one",
}

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

func TestEmailMatchesAnyPattern(t *testing.T) {
	patterns := []string{
		"@eftestbot.com",
		"exact@example.com",
	}
	patterns = append(patterns, controlledPGCTestPatterns...)
	cases := []struct {
		email string
		want  bool
	}{
		{"bot1@eftestbot.com", true},
		{"kairui1@pgc.eigenflux.one", true},
		{"KAIRUI12@PGC.EIGENFLUX.ONE", true},
		{"  kairui9@pgc.eigenflux.one  ", true},
		{"weici0@pgc.eigenflux.one", true},
		{"weici99@pgc.eigenflux.one", true},
		{"lingan7@pgc.eigenflux.one", true},
		{"lingan42@pgc.eigenflux.one", true},
		{"vic0@pgc.eigenflux.one", true},
		{"vic99@pgc.eigenflux.one", true},
		{"exact@example.com", true},
		{"kairui@pgc.eigenflux.one", false},
		{"kairuia@pgc.eigenflux.one", false},
		{"kairui123@pgc.eigenflux.one", false},
		{"kairui09@pgc.eigenflux.one", false},
		{"xkairui1@pgc.eigenflux.one", false},
		{"kairui1@other.example", false},
	}
	for _, c := range cases {
		if got := EmailMatchesAnyPattern(c.email, patterns); got != c.want {
			t.Errorf("EmailMatchesAnyPattern(%q) = %v, want %v", c.email, got, c.want)
		}
	}
	if EmailMatchesAnyPattern("anything@example.com", []string{"[invalid"}) {
		t.Error("invalid glob patterns must fail closed")
	}
}

func TestControlledPGCTestPatternsCoverZeroThroughNinetyNine(t *testing.T) {
	for _, prefix := range []string{"kairui", "lingan", "weici", "vic"} {
		for suffix := 0; suffix <= 99; suffix++ {
			email := fmt.Sprintf("%s%d@pgc.eigenflux.one", prefix, suffix)
			if !EmailMatchesAnyPattern(email, controlledPGCTestPatterns) {
				t.Fatalf("controlled test account did not match: %s", email)
			}
		}
		for _, invalid := range []string{
			prefix + "@pgc.eigenflux.one",
			prefix + "00@pgc.eigenflux.one",
			prefix + "09@pgc.eigenflux.one",
			prefix + "100@pgc.eigenflux.one",
		} {
			if EmailMatchesAnyPattern(invalid, controlledPGCTestPatterns) {
				t.Fatalf("invalid controlled test account matched: %s", invalid)
			}
		}
	}
}
