package cmd

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFeedBatchPollOutputSize(t *testing.T) {
	page := largeFeedBatchFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/agent-context":
			_, _ = fmt.Fprint(w, `{"data":{"context_revision":1,"control_context":{"context_revision":1}}}`)
		case "/api/v2/runtime/heartbeat":
			_, _ = fmt.Fprint(w, `{"data":{}}`)
		case "/api/v2/feed":
			_, _ = fmt.Fprintf(w, `{"data":%s}`, page)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	_, server := runtimeTestConfig(t, srv.URL, true)
	old := formatFlag
	formatFlag = "agent"
	t.Cleanup(func() { formatFlag = old })
	cmd := batchTestCommand(5)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := pollFeedV2(cmd, server, "20"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if len(out) > 32*1024 || strings.Contains(out, "SERVED CONTRACT:") || strings.Contains(out, "author-5") || !strings.Contains(out, `"instruction_ref"`) {
		t.Fatalf("poll output (%d bytes): %s", len(out), out)
	}
	t.Logf("feed poll --batch 5 --format agent: %d bytes", len(out))
}
