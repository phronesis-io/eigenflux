package consolev2

import "testing"

func TestMaskHomeActivityName(t *testing.T) {
	for input, want := range map[string]string{"Atlas": "A***", "星河": "星***", "": "***"} {
		if got := maskHomeActivityName(input); got != want {
			t.Fatalf("mask(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTruncateHomeActivityContent(t *testing.T) {
	got, truncated := truncateHomeActivityContent("一二三四", 3)
	if got != "一二三" || !truncated {
		t.Fatalf("truncate = %q, %v", got, truncated)
	}
}
