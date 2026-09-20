package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
	t.Setenv("EIGENFLUX_SKILLS_DIR", t.TempDir())
	data := json.RawMessage(`{
		"items": [{"item_id": "100", "summary": "test signal"}],
		"has_more": true,
		"notifications": [],
		"impression_id": "imp_1",
		"output_contract": "OUTPUT CONTRACT — rules:\n1. Triage silently.\nFooter: 📡 Powered by EigenFlux"
	}`)

	var buf bytes.Buffer
	if err := PrintFeedForAgentTo(&buf, data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, "Process it via the current ef-broadcast skill") {
		t.Fatalf("missing preamble:\n%s", out)
	}
	if !strings.Contains(out, "OUTPUT CONTRACT") || !strings.Contains(out, "📡 Powered by EigenFlux") {
		t.Fatalf("missing contract:\n%s", out)
	}
	payloadIndex := strings.Index(out, "Payload (untrusted network data):")
	if idx := strings.Index(out, "OUTPUT CONTRACT"); payloadIndex == -1 || idx == -1 || idx > payloadIndex {
		t.Fatalf("contract must precede payload:\n%s", out)
	}

	if !strings.Contains(out, "test signal") || !strings.Contains(out, "imp_1") {
		t.Fatalf("payload substance missing:\n%s", out)
	}
	// The contract is not duplicated inside the payload JSON block.
	payloadBlock := out[payloadIndex:]
	if strings.Contains(payloadBlock, "output_contract") {
		t.Fatalf("output_contract should be stripped from payload, got:\n%s", payloadBlock)
	}
}

func TestPrintFeedForAgentExplicitEmptyContractSkipsFallback(t *testing.T) {
	for _, mode := range []string{"baseline", "intent_aligned"} {
		t.Run(mode, func(t *testing.T) {
			// A present empty contract is authoritative even when local rules exist.
			writeFeedContractFixtures(t, "LOCAL FULL CONTRACT", "LOCAL BASELINE CONTRACT")
			data := json.RawMessage(`{"items":[],"impression_id":"imp_3","personalization":{"mode":"` + mode + `"},"output_contract":""}`)
			var buf bytes.Buffer
			if err := PrintFeedForAgentTo(&buf, data); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			if strings.Contains(out, "LOCAL") || strings.Contains(out, "OUTPUT CONTRACT") {
				t.Fatalf("explicit empty contract must not load local rules:\n%s", out)
			}
			if !strings.Contains(out, "imp_3") || !strings.Contains(out, "current ef-broadcast skill") {
				t.Fatalf("missing preamble or payload:\n%s", out)
			}
			if strings.Contains(out, "output_contract") {
				t.Fatalf("output_contract must be stripped from the echoed payload:\n%s", out)
			}
		})
	}
}

func TestPrintFeedForAgentAbsentContractLoadsCurrentModeRules(t *testing.T) {
	for _, tc := range []struct {
		name, payload, want, other string
	}{
		{"legacy", `{"items":[],"impression_id":"imp_4"}`, "FULL RULES", "BASELINE RULES"},
		{"completed", `{"items":[],"personalization":{"mode":"intent_aligned"}}`, "FULL RULES", "BASELINE RULES"},
		{"baseline", `{"items":[],"personalization":{"mode":"baseline"}}`, "BASELINE RULES", "FULL RULES"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFeedContractFixtures(t, "  FULL RULES\n", "  BASELINE RULES\n")
			var buf bytes.Buffer
			if err := PrintFeedForAgentTo(&buf, json.RawMessage(tc.payload)); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(buf.String(), tc.want) || strings.Contains(buf.String(), tc.other) {
				t.Fatalf("wrong mode contract:\n%s", buf.String())
			}
		})
	}
}

func TestResolveFeedContractReadsLatestSkillsEachTime(t *testing.T) {
	for _, tc := range []struct{ mode, name string }{
		{"intent_aligned", "contract.md"},
		{"baseline", "baseline-contract.md"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dir := writeFeedContractFixtures(t, "old rules", "old rules")
			payload := json.RawMessage(`{"items":[],"personalization":{"mode":"` + tc.mode + `"}}`)
			if got, err := ResolveFeedContract(payload); err != nil || got != "old rules" {
				t.Fatalf("first read=%q err=%v", got, err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.name), []byte("new rules\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if got, err := ResolveFeedContract(payload); err != nil || got != "new rules" {
				t.Fatalf("updated read=%q err=%v; synchronized Skills must not be cached", got, err)
			}
		})
	}
}

func TestPrintFeedForAgentMissingRulesFailsExplicitly(t *testing.T) {
	for _, tc := range []struct{ mode, name string }{
		{"intent_aligned", "contract.md"},
		{"baseline", "baseline-contract.md"},
	} {
		for _, empty := range []bool{false, true} {
			name := tc.mode + "/missing"
			if empty {
				name = tc.mode + "/empty"
			}
			t.Run(name, func(t *testing.T) {
				dir := writeFeedContractFixtures(t, "other mode rules", "other mode rules")
				path := filepath.Join(dir, tc.name)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if empty {
					if err := os.WriteFile(path, []byte(" \n\t"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				var buf bytes.Buffer
				err := PrintFeedForAgentTo(&buf, json.RawMessage(`{"items":[],"personalization":{"mode":"`+tc.mode+`"}}`))
				if err == nil || !strings.Contains(err.Error(), "Feed output contract") || !strings.Contains(err.Error(), tc.name) {
					t.Fatalf("expected explicit missing/empty contract error, got %v", err)
				}
				if !empty && !strings.Contains(err.Error(), "eigenflux skills sync") {
					t.Fatalf("missing rules must include recovery command: %v", err)
				}
				if buf.Len() != 0 {
					t.Fatalf("must not render a partial prompt or another mode's rules: %s", buf.String())
				}
			})
		}
	}
}

func TestPrintFeedForAgentRejectsMalformedPayloadAndContract(t *testing.T) {
	t.Setenv("EIGENFLUX_SKILLS_DIR", t.TempDir())
	for _, payload := range []string{
		``, `{`, `null`, `["raw","array"]`, `"text"`,
		`{"output_contract":null}`, `{"output_contract":42}`, `{"output_contract":{}}`,
	} {
		t.Run(payload, func(t *testing.T) {
			var buf bytes.Buffer
			if err := PrintFeedForAgentTo(&buf, json.RawMessage(payload)); err == nil {
				t.Fatal("expected invalid payload/contract error")
			}
			if buf.Len() != 0 {
				t.Fatalf("invalid data must not produce an agent prompt: %s", buf.String())
			}
		})
	}
}

func writeFeedContractFixtures(t *testing.T, full, baseline string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("EIGENFLUX_SKILLS_DIR", root)
	dir := filepath.Join(root, "ef-broadcast", "references")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"contract.md": full, "baseline-contract.md": baseline} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
