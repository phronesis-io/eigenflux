package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLimitItemDetailContent(t *testing.T) {
	data := json.RawMessage(`{"item":{"content":"` + strings.Repeat("界", 6) + `","item_id":"1"}}`)

	got, err := limitItemDetailContent(data, 5)
	if err != nil {
		t.Fatalf("limitItemDetailContent: %v", err)
	}
	var payload struct {
		Item struct {
			Content          string `json:"content"`
			ContentTruncated bool   `json:"content_truncated"`
		} `json:"item"`
	}
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Item.Content != "界界界界…" || !payload.Item.ContentTruncated {
		t.Fatalf("unexpected bounded detail: %+v", payload.Item)
	}
}

func TestLimitItemDetailContentMarksComplete(t *testing.T) {
	got, err := limitItemDetailContent(json.RawMessage(`{"item":{"content":"short"}}`), 10)
	if err != nil {
		t.Fatalf("limitItemDetailContent: %v", err)
	}
	if !strings.Contains(string(got), `"content_truncated":false`) {
		t.Fatalf("missing non-truncated signal: %s", got)
	}
}
