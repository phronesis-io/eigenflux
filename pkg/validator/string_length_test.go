package validator

import (
	"testing"
)

func TestCalculateMultilingualLength(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{
			name:     "pure ASCII",
			input:    "hello",
			expected: 5,
		},
		{
			name:     "pure Chinese",
			input:    "你好",
			expected: 4,
		},
		{
			name:     "mixed English and Chinese",
			input:    "hello你好",
			expected: 9,
		},
		{
			name:     "Japanese Hiragana",
			input:    "こんにちは",
			expected: 10,
		},
		{
			name:     "Japanese Katakana",
			input:    "カタカナ",
			expected: 8,
		},
		{
			name:     "Korean Hangul",
			input:    "안녕하세요",
			expected: 10,
		},
		{
			name:     "mixed with numbers and symbols",
			input:    "测试123!@#",
			expected: 10,
		},
		{
			name:     "empty string",
			input:    "",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateMultilingualLength(tt.input)
			if result != tt.expected {
				t.Errorf("CalculateMultilingualLength(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestValidateStringLength(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		maxLength int
		expected  bool
	}{
		{
			name:      "within limit - ASCII",
			input:     "hello",
			maxLength: 10,
			expected:  true,
		},
		{
			name:      "within limit - Chinese",
			input:     "你好",
			maxLength: 4,
			expected:  true,
		},
		{
			name:      "exceeds limit - Chinese",
			input:     "你好世界",
			maxLength: 7,
			expected:  false,
		},
		{
			name:      "exactly at limit",
			input:     "hello你好",
			maxLength: 9,
			expected:  true,
		},
		{
			name:      "exceeds limit by 1",
			input:     "hello你好",
			maxLength: 8,
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateStringLength(tt.input, tt.maxLength)
			if result != tt.expected {
				t.Errorf("ValidateStringLength(%q, %d) = %v, want %v", tt.input, tt.maxLength, result, tt.expected)
			}
		})
	}
}

func TestValidateBroadcastContent(t *testing.T) {
	if err := ValidateBroadcastContent("hello"); err != nil {
		t.Fatalf("short content rejected: %v", err)
	}
	if err := ValidateBroadcastContent(" \n\t"); err != ErrBroadcastContentRequired {
		t.Fatalf("blank content error = %v", err)
	}
	if err := ValidateBroadcastContent(string(make([]byte, MaxBroadcastContentLength+1))); err != ErrBroadcastContentTooLong {
		t.Fatalf("oversized content error = %v", err)
	}
}
