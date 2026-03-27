package consumer

import (
	"testing"
)

func TestMatchBlacklist(t *testing.T) {
	tests := []struct {
		name      string
		keywords  []string
		content   string
		url       string
		notes     string
		wantMatch string
	}{
		{
			name:      "exact match in content",
			keywords:  []string{"spam"},
			content:   "This is spam content",
			wantMatch: "spam",
		},
		{
			name:      "case insensitive match",
			keywords:  []string{"SPAM"},
			content:   "This is spam content",
			wantMatch: "SPAM",
		},
		{
			name:      "match in URL",
			keywords:  []string{"malware"},
			url:       "http://malware-site.com",
			wantMatch: "malware",
		},
		{
			name:      "match in notes",
			keywords:  []string{"phishing"},
			notes:     "This looks like phishing",
			wantMatch: "phishing",
		},
		{
			name:      "no match",
			keywords:  []string{"spam", "malware"},
			content:   "Normal good content",
			wantMatch: "",
		},
		{
			name:      "empty keywords",
			keywords:  nil,
			content:   "Any content",
			wantMatch: "",
		},
		{
			name:      "first matching keyword returned",
			keywords:  []string{"good", "bad", "ugly"},
			content:   "This is bad and ugly",
			wantMatch: "bad",
		},
		{
			name:      "substring match",
			keywords:  []string{"crypto"},
			content:   "Buy cryptocurrency now!",
			wantMatch: "crypto",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchBlacklist(tt.keywords, tt.content, tt.url, tt.notes)
			if got != tt.wantMatch {
				t.Errorf("matchBlacklist() = %q, want %q", got, tt.wantMatch)
			}
		})
	}
}
