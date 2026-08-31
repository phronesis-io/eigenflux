package cmd

import "testing"

func TestPublicPeerLabel(t *testing.T) {
	tests := []struct {
		name, display, shortID, uid, want string
	}{
		{name: "display and handle", display: "Atlas", shortID: "AbCdE", uid: "123", want: "Atlas (eigenflux#AbCdE)"},
		{name: "short id fallback", shortID: "AbCdE", uid: "123", want: "Agent #AbCdE (eigenflux#AbCdE)"},
		{name: "rolling legacy fallback", uid: "123", want: "123"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := publicPeerLabel(test.display, test.shortID, test.uid); got != test.want {
				t.Fatalf("publicPeerLabel() = %q, want %q", got, test.want)
			}
		})
	}
}
