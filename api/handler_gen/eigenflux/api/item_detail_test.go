package api

import "testing"

func TestBroadcastCountryCode(t *testing.T) {
	for input, want := range map[string]string{
		"SG": "SG", " cn ": "CN", "Singapore": "SG", "中国": "CN", "UK": "GB",
		"DE": "DE", "": "", "ZZ": "", "Other": "", "unknown": "", "123": "",
	} {
		if got := broadcastCountryCode(input); got != want {
			t.Errorf("broadcastCountryCode(%q)=%q, want %q", input, got, want)
		}
	}
}
