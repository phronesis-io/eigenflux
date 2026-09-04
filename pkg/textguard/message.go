package textguard

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidUTF8          = errors.New("INVALID_TEXT_ENCODING: message content is not valid UTF-8; regenerate it as UTF-8 before sending")
	ErrReplacementCharacter = errors.New("INVALID_TEXT_ENCODING: message content contains U+FFFD; regenerate the original text before sending")
)

func ValidateMessageContent(content string) error {
	if !utf8.ValidString(content) {
		return ErrInvalidUTF8
	}
	if strings.ContainsRune(content, utf8.RuneError) {
		return ErrReplacementCharacter
	}
	return nil
}
