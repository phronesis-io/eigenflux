package runtimeidentity

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		input  string
		want   Identity
		wantOK bool
	}{
		{"openclaw/0.0.30", Identity{Name: "openclaw", Version: "0.0.30", IsPlugin: true}, true},
		{"Claude-Code/1.2.3", Identity{Name: "claude-code", Version: "1.2.3", IsPlugin: true}, true},
		{"jarvis", Identity{Name: "jarvis"}, true},
		{"hermes/controlled-adapter-0.3.1", Identity{Name: "hermes", Version: "controlled-adapter-0.3.1"}, true},
		{"workbuddy/2026.08", Identity{Name: "workbuddy", Version: "2026.08"}, true},
		{"terminal", Identity{}, false},
		{"bad host/1", Identity{}, false},
		{"codex/<script>", Identity{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := Parse(tt.input)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("Parse(%q) = (%+v,%v), want (%+v,%v)", tt.input, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
