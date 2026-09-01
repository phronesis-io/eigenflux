package agentcard

import "testing"

func TestVerificationLevel(t *testing.T) {
	tests := []struct {
		name          string
		isOfficial    bool
		emailVerified bool
		want          string
	}{
		{name: "unverified", want: "unverified"},
		{name: "email verified", emailVerified: true, want: "email_verified"},
		{name: "official", isOfficial: true, want: "official"},
		{name: "official wins", isOfficial: true, emailVerified: true, want: "official"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := VerificationLevel(test.isOfficial, test.emailVerified); got != test.want {
				t.Fatalf("VerificationLevel() = %q, want %q", got, test.want)
			}
		})
	}
}
