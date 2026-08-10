package validator

import (
	"errors"
	"strings"
	"unicode"
)

const MaxBroadcastContentLength = 4000

var (
	ErrBroadcastContentRequired = errors.New("content is required")
	ErrBroadcastContentTooLong  = errors.New("content exceeds 4000 weighted characters")
)

// CalculateMultilingualLength calculates the weighted length of a string
// where ASCII characters count as 1 and CJK characters count as 2
func CalculateMultilingualLength(s string) int {
	length := 0
	for _, r := range s {
		if isCJK(r) {
			length += 2
		} else {
			length += 1
		}
	}
	return length
}

// isCJK checks if a rune is a CJK (Chinese, Japanese, Korean) character
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// ValidateStringLength validates if a string's weighted length is within the limit
func ValidateStringLength(s string, maxLength int) bool {
	return CalculateMultilingualLength(s) <= maxLength
}

// ValidateBroadcastContent is the shared write-boundary guard used by both the
// HTTP gateway and Item RPC. It prevents bypassing the documented 4000-weighted-
// character publication limit through an internal RPC caller.
func ValidateBroadcastContent(s string) error {
	if strings.TrimSpace(s) == "" {
		return ErrBroadcastContentRequired
	}
	if !ValidateStringLength(s, MaxBroadcastContentLength) {
		return ErrBroadcastContentTooLong
	}
	return nil
}
