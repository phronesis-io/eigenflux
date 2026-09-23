package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNeedInputRequiresRetryKeyBeforeIO(t *testing.T) {
	command := newNeedCommand()
	command.SetArgs([]string{"input", "create", "--file", "missing"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "idempotency-key") {
		t.Fatalf("%v", err)
	}
}
func TestNeedFilePreservesRawInputAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "need.json")
	raw := []byte(`{"schema_version":"need_input.v1","intent_id":"123","intent_version":1,"need_type":"commission","target":{"desc":" Keep original wording ","candidate_needs":["database tuning"]},"constraints":{"budget_max_fen":0,"currency":"CNY"}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readNeedFile(path)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("rewritten payload: %s %v", got, err)
	}
	for _, bad := range []string{"null", "[]", "{} {}", strings.Repeat(" ", 32769)} {
		_ = os.WriteFile(path, []byte(bad), 0600)
		if _, err := readNeedFile(path); err == nil {
			t.Fatalf("accepted invalid file")
		}
	}
	for _, id := range []string{"0", "-1", "01", "1/../2", "9223372036854775808"} {
		if validateNeedID(id) == nil {
			t.Fatalf("accepted ID %s", id)
		}
	}
}
