package logger

import (
	"strings"
)

// MaskEmail returns a partially masked email suitable for operational logs.
// It keeps up to the first two characters of the local-part plus the full
// domain, for example "ce****@example.com".
func MaskEmail(value string) string {
	email := strings.TrimSpace(strings.ToLower(value))
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		if len(email) <= 2 {
			return email + "****"
		}
		return email[:2] + "****"
	}

	localPart := email[:at]
	domain := email[at+1:]
	visible := min(2, len(localPart))
	return localPart[:visible] + "****@" + domain
}
