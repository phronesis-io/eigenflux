package cmd

import (
	"strings"
	"testing"
)

func TestValidateMessageContent(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantReason string
	}{
		{name: "valid", content: "你好, EigenFlux — hello"},
		{name: "invalid UTF-8", content: string([]byte{0xff}), wantReason: "not valid UTF-8"},
		{name: "replacement character", content: "损坏�内容", wantReason: "contains U+FFFD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMessageContent(tt.content)
			if tt.wantReason == "" && err != nil {
				t.Fatalf("valid content rejected: %v", err)
			}
			if tt.wantReason != "" && (err == nil || !strings.Contains(err.Error(), tt.wantReason)) {
				t.Fatalf("error = %v, want reason %q", err, tt.wantReason)
			}
		})
	}
}
