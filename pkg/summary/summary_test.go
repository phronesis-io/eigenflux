package summary

import (
	"strings"
	"testing"
)

func TestPublisherOwnership(t *testing.T) {
	for _, tc := range []struct {
		notes, want string
		invalid     bool
	}{
		{`{"summary":"  FTC: v0.4 is NOT proven safe  "}`, "  FTC: v0.4 is NOT proven safe  ", false},
		{`{}`, "", false}, {`{"summary":null}`, "", false}, {`{"summary":"  \n "}`, "", false}, {"", "", false},
		{`{"summary":3}`, "", true}, {`[]`, "", true}, {`{"summary":"` + strings.Repeat("界", 281) + `"}`, "", true},
		{`{"summary":"` + strings.Repeat("界", 280) + `"}`, strings.Repeat("界", 280), false},
	} {
		got, err := Publisher(tc.notes)
		if (err != nil) != tc.invalid || got != tc.want {
			t.Errorf("Publisher(%q)=%q,%v", tc.notes, got, err)
		}
	}
}
func TestPreviewKeepsSummaryAndBodyDistinct(t *testing.T) {
	p := MakePreview("FTC v0.4", "body has more facts", "publisher", 800)
	if p.Text != "FTC v0.4" || p.Source != "summary" || p.SummaryOrigin != "publisher" || p.Truncated {
		t.Fatal(p)
	}
	p = MakePreview("", "一二三", "none", 2)
	if p.Text != "一二" || p.Source != "content" || p.SummaryOrigin != "none" || !p.Truncated {
		t.Fatal(p)
	}
	if Origin("") != "unknown" {
		t.Fatal("legacy origin inferred")
	}
	if TranslationKey("old") == TranslationKey("new") {
		t.Fatal("stale translation key")
	}
}
