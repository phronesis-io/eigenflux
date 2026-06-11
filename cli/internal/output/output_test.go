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
