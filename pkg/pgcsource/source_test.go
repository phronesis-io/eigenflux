package pgcsource

import "testing"

func TestGraceKimLegacyAndStructured(t *testing.T) {
	title := "Election ‘war rooms’ enter the AI age"
	ts := "2026-11-02T09:45:37+00:00"
	body := "Both OpenAI and Anthropic will have “war rooms”."
	header := titleLabel + title + "\n" + timeLabel + ts
	for _, h := range []string{header, titleLabel + title, timeLabel + ts} {
		got, f := Read(h+"\n\n"+body, "")
		if got != body || f.Title == "" && f.Timestamp == "" {
			t.Fatal(got, f)
		}
	}
	notes := `{"summary":"example","pgc_source":{"title":"Election ‘war rooms’ enter the AI age","timestamp":"2026-11-02T09:45:37+00:00","model_prefix":"Source title: Election ‘war rooms’ enter the AI age\nSource timestamp (UTC): 2026-11-02T09:45:37+00:00"}}`
	got, f := Read(body, notes)
	model, clean := f.ModelInput(body, notes)
	if got != body || f.Title != title || f.Timestamp != ts || model != header+"\n\n"+body || clean != `{"summary":"example"}` {
		t.Fatal(got, f, model, clean)
	}
	for _, raw := range []string{"Source timestamp (UTC): unknown\n\narticle", "Source title: hello\nthird line\n\narticle", header + "\n\n"} {
		if got, _ := Read(raw, ""); got != raw {
			t.Fatal(got)
		}
	}
}
