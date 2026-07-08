package official

import (
	"strings"
	"testing"
)

func TestDirectiveForLang(t *testing.T) {
	if got := DirectiveForLang(""); got != "" {
		t.Fatalf("empty preference must add no directive, got %q", got)
	}
	if got := DirectiveForLang("fr"); got != "" {
		t.Fatalf("unknown preference must add no directive, got %q", got)
	}
	if got := DirectiveForLang("zh"); !strings.Contains(got, "Simplified Chinese") {
		t.Fatalf("zh directive must pin Simplified Chinese, got %q", got)
	}
	if got := DirectiveForLang("en"); !strings.Contains(got, "English") {
		t.Fatalf("en directive must pin English, got %q", got)
	}
	// Directives are appended to task strings; they must lead with a space so
	// they never fuse with the preceding sentence.
	for _, lang := range []string{"zh", "en"} {
		if !strings.HasPrefix(DirectiveForLang(lang), " ") {
			t.Fatalf("%s directive must start with a space", lang)
		}
	}
}
