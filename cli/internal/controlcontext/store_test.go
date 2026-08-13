package controlcontext

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadSnapshot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	want := Snapshot{Revision: 9, Context: json.RawMessage(`{"network_goal":{"text":"test"}}`)}
	if err := Save("test", want); err != nil {
		t.Fatal(err)
	}
	got, err := Load("test")
	if err != nil || got.Revision != want.Revision || !bytes.Equal(compactJSON(got.Context), compactJSON(want.Context)) {
		t.Fatalf("load=%#v err=%v", got, err)
	}
	info, err := os.Stat(filepath.Join(dir, ".eigenflux", "servers", "test", "control-context.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("context cache mode=%v", info.Mode().Perm())
	}
}

func compactJSON(value []byte) []byte {
	var out bytes.Buffer
	_ = json.Compact(&out, value)
	return out.Bytes()
}
