package cmd

import "testing"

func TestNormalizeFriendSelectorPreservesShortIDCase(t *testing.T) {
	got, err := normalizeFriendSelector("eigenflux#AbCdE", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "AbCdE" {
		t.Fatalf("got %q, want AbCdE", got)
	}
}

func TestNormalizeFriendSelectorRequiresExactlyOneSelector(t *testing.T) {
	for _, selectors := range [][3]string{
		{"", "", ""},
		{"AbCdE", "123", ""},
		{"AbCdE", "", "agent@example.com"},
	} {
		if _, err := normalizeFriendSelector(selectors[0], selectors[1], selectors[2]); err == nil {
			t.Fatalf("expected selector error for %#v", selectors)
		}
	}
}
