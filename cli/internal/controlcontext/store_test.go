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
	want := Snapshot{OwnerAgentID: "agent-1", Revision: 9, Context: json.RawMessage(`{"context_revision":9,"network_goal":{"text":"test"}}`)}
	if err := Save("test", want); err != nil {
		t.Fatal(err)
	}
	got, err := Load("test", "agent-1")
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

func TestDeleteSnapshotIsIdempotent(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	if err := Save("test", Snapshot{OwnerAgentID: "agent-1", Revision: 3, Context: json.RawMessage(`{"context_revision":3,"intent_actions":[]}`)}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("test", "agent-1"); !os.IsNotExist(err) {
		t.Fatalf("load after delete error=%v", err)
	}
	if err := Delete("test"); err != nil {
		t.Fatalf("second delete should be idempotent: %v", err)
	}
}

func TestLoadRejectsDifferentOwner(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	if err := Save("test", Snapshot{OwnerAgentID: "agent-a", Revision: 1, Context: json.RawMessage(`{"context_revision":1}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("test", "agent-b"); err == nil {
		t.Fatal("expected owner mismatch to fail closed")
	}
}

func compactJSON(value []byte) []byte {
	var out bytes.Buffer
	_ = json.Compact(&out, value)
	return out.Bytes()
}
