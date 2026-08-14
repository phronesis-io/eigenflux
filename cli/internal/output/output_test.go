package output

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestFormatResolution(t *testing.T) {
	f := ResolveFormat("json")
	if f != "json" {
		t.Errorf("got %q, want json", f)
	}
	f = ResolveFormat("table")
	if f != "table" {
		t.Errorf("got %q, want table", f)
	}
}

func TestPrintDataJSON(t *testing.T) {
	var buf bytes.Buffer
	data := map[string]string{"name": "test"}
	PrintDataTo(&buf, data, "json")
	var parsed map[string]string
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if parsed["name"] != "test" {
		t.Errorf("name = %q, want %q", parsed["name"], "test")
	}
}

func TestExitCodes(t *testing.T) {
	if ExitSuccess != 0 {
		t.Errorf("ExitSuccess = %d, want 0", ExitSuccess)
	}
	if ExitAuthRequired != 4 {
		t.Errorf("ExitAuthRequired = %d, want 4", ExitAuthRequired)
	}
}

func TestIsTTY(t *testing.T) {
	if IsTTY(os.Stdout) {
		t.Log("stdout is a TTY (unexpected in CI, ok locally)")
	}
}

func TestPrintFeedForAgentLeadsWithContract(t *testing.T) {
	data := json.RawMessage(`{
		"items": [{"item_id": "100", "summary": "test signal"}],
		"has_more": true,
		"notifications": [],
		"impression_id": "imp_1",
		"output_contract": "OUTPUT CONTRACT — rules:\n1. Triage silently.\nFooter: 📡 Powered by EigenFlux"
	}`)

	var buf bytes.Buffer
	PrintFeedForAgentTo(&buf, data)
	out := buf.String()

	if !strings.Contains(out, "Process it via the ef-broadcast skill") {
		t.Fatalf("missing preamble:\n%s", out)
	}
	if !strings.Contains(out, "OUTPUT CONTRACT") || !strings.Contains(out, "📡 Powered by EigenFlux") {
		t.Fatalf("missing contract:\n%s", out)
	}
	if idx := strings.Index(out, "OUTPUT CONTRACT"); idx == -1 || idx > strings.Index(out, "Payload:") {
		t.Fatalf("contract must precede payload:\n%s", out)
	}

	if !strings.Contains(out, "test signal") || !strings.Contains(out, "imp_1") {
		t.Fatalf("payload substance missing:\n%s", out)
	}
	// The contract is not duplicated inside the payload JSON block.
	payloadBlock := out[strings.Index(out, "Payload:"):]
	if strings.Contains(payloadBlock, "output_contract") {
		t.Fatalf("output_contract should be stripped from payload, got:\n%s", payloadBlock)
	}
}

func TestPrintFeedForAgentWithoutContractStillRenders(t *testing.T) {
	data := json.RawMessage(`{"items": [], "has_more": false, "notifications": [], "impression_id": "imp_2"}`)

	var buf bytes.Buffer
	PrintFeedForAgentTo(&buf, data)
	out := buf.String()

	if !strings.Contains(out, "Process it via the ef-broadcast skill") {
		t.Fatalf("missing preamble:\n%s", out)
	}
	if !strings.Contains(out, "imp_2") {
		t.Fatalf("payload missing:\n%s", out)
	}
}

func TestPrintFeedForAgentExplicitEmptyContractSkipsFallback(t *testing.T) {
	// An empty-but-present output_contract is the server declining to bind any
	// output rules for this payload (the common empty-poll case). Falling back
	// to the embedded copy here would reinstate the rules it just withheld.
	data := json.RawMessage(`{"items": [], "notifications": [], "impression_id": "imp_3", "output_contract": ""}`)

	var buf bytes.Buffer
	PrintFeedForAgentTo(&buf, data)
	out := buf.String()

	if strings.Contains(out, "OUTPUT CONTRACT") {
		t.Fatalf("explicit empty contract must not fall back to the embedded copy:\n%s", out)
	}
	if !strings.Contains(out, "Process it via the ef-broadcast skill") {
		t.Fatalf("missing preamble:\n%s", out)
	}
	if !strings.Contains(out, "imp_3") {
		t.Fatalf("payload missing:\n%s", out)
	}
	if strings.Contains(out, "output_contract") {
		t.Fatalf("output_contract must be stripped from the echoed payload:\n%s", out)
	}
}

func TestPrintFeedForAgentAbsentContractStillFallsBack(t *testing.T) {
	// Field absent = old server with no contract to give; the embedded copy
	// must still bind so plugin-less runtimes stay bound.
	data := json.RawMessage(`{"items": [{"item_id":"1"}], "impression_id": "imp_4"}`)

	var buf bytes.Buffer
	PrintFeedForAgentTo(&buf, data)

	if !strings.Contains(buf.String(), "OUTPUT CONTRACT") {
		t.Fatalf("absent contract must fall back to the embedded copy:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "raw_content_truncated=true") ||
		!strings.Contains(buf.String(), "eigenflux feed get --item-id") ||
		!strings.Contains(buf.String(), "do not retry in the same poll/cycle") {
		t.Fatalf("fallback must bind gated full-content fetching without retry storms:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "finish with exactly NO_REPLY") ||
		!strings.Contains(buf.String(), "Never return an empty assistant turn") {
		t.Fatalf("fallback must encode intentional silent success without an empty turn:\n%s", buf.String())
	}
}

func TestPrintFeedForAgentEchoesNonObjectPayloadVerbatim(t *testing.T) {
	// A non-object top-level payload must be passed through, not dropped to "{}".
	data := json.RawMessage(`["raw","array","payload"]`)

	var buf bytes.Buffer
	PrintFeedForAgentTo(&buf, data)
	out := buf.String()

	if !strings.Contains(out, `"raw"`) || !strings.Contains(out, `"payload"`) {
		t.Fatalf("non-object payload should be echoed verbatim, got:\n%s", out)
	}
	if strings.Contains(out, "{}") {
		t.Fatalf("payload was dropped to empty object:\n%s", out)
	}
}
