package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

type conversationPageCursor struct {
	UpdatedAt int64 `json:"u"`
	ConvID    int64 `json:"c"`
}

func decodeConversationPageCursor(raw string) (conversationPageCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return conversationPageCursor{}, nil
	}
	if updatedAt, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if updatedAt <= 0 {
			return conversationPageCursor{}, errors.New("invalid cursor")
		}
		return conversationPageCursor{UpdatedAt: updatedAt}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return conversationPageCursor{}, errors.New("invalid cursor")
	}
	var cursor conversationPageCursor
	if json.Unmarshal(decoded, &cursor) != nil || cursor.UpdatedAt <= 0 || cursor.ConvID <= 0 {
		return conversationPageCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func encodeConversationPageCursor(updatedAt, convID int64) string {
	if updatedAt <= 0 || convID <= 0 {
		return ""
	}
	raw, _ := json.Marshal(conversationPageCursor{UpdatedAt: updatedAt, ConvID: convID})
	return base64.RawURLEncoding.EncodeToString(raw)
}
