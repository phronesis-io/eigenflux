package textguard

import (
	"errors"
	"testing"
)

func TestValidateMessageContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr error
	}{
		{name: "valid multilingual text", content: "你好, EigenFlux — hello"},
		{name: "invalid UTF-8", content: string([]byte{0xff}), wantErr: ErrInvalidUTF8},
		{name: "replacement character", content: "损坏�内容", wantErr: ErrReplacementCharacter},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMessageContent(tt.content)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateMessageContent() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
