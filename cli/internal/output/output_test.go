package output

import (
	"bytes"
	"encoding/json"
	"os"
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
