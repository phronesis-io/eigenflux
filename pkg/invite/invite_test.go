package invite

import (
	"strings"
	"testing"
)

func TestNewCodeFormat(t *testing.T) {
	seen := make(map[string]bool)
	for range 200 {
		c := NewCode()
		if !ValidFormat(c) {
			t.Fatalf("generated code %q does not match EFI-xxxxxx", c)
		}
		if seen[c] {
			t.Fatalf("duplicate code generated in 200 draws: %s", c)
		}
		seen[c] = true
	}
}

func TestValidFormat(t *testing.T) {
	cases := map[string]bool{
		"EFI-a1B2c3":   true,
		"EFI-000000":   true,
		"EF-a1B2c3d4":  false, // install token, not an invite code
		"EFI-a1B2c":    false, // too short
		"EFI-a1B2c3d":  false, // too long
		"EFI-a1B2c!":   false,
		"efi-a1B2c3":   false, // prefix is case-sensitive
		"":             false,
		" EFI-a1B2c3 ": false,
	}
	for in, want := range cases {
		if got := ValidFormat(in); got != want {
			t.Errorf("ValidFormat(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNormalizeChannelName(t *testing.T) {
	if got := NormalizeChannelName("  RedSkills "); got != "redskills" {
		t.Errorf("expected lowercase trimmed name, got %q", got)
	}
	long := NormalizeChannelName(strings.Repeat("x", 50))
	if len(long) != 32 {
		t.Errorf("expected 32-char cap, got %d", len(long))
	}
}

func TestIsInternalEmail(t *testing.T) {
	for email, want := range map[string]bool{
		"alice@example.com":     false,
		"x1@bot.eigenflux.ai":   true,
		"y2@PGC.eigenflux.ai":   true,
		"kol@redskills.example": false,
	} {
		if got := IsInternalEmail(email); got != want {
			t.Errorf("IsInternalEmail(%q) = %v, want %v", email, got, want)
		}
	}
}

func TestTokenChannel(t *testing.T) {
	kol := &Code{Kind: KindKOL, Name: ""}
	if kol.TokenChannel() != "kol" {
		t.Errorf("KOL code should bucket as kol, got %q", kol.TokenChannel())
	}
	ch := &Code{Kind: KindChannel, Name: "redskills"}
	if ch.TokenChannel() != "redskills" {
		t.Errorf("channel code should bucket as its name, got %q", ch.TokenChannel())
	}
}
