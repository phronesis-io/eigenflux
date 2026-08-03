package profilestate

import (
	"os"
	"testing"
)

func TestStateRoundTripAndScopeIsolation(t *testing.T) {
	home := t.TempDir()
	want := State{LastRefreshUnix: 11, LastCheckedUnix: 22}
	if err := Save(home, "prod", "101", want); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := Load(home, "prod", "101"); got != want {
		t.Fatalf("load = %+v, want %+v", got, want)
	}
	if got := Load(home, "prod", "202"); got != (State{}) {
		t.Fatalf("another account inherited state: %+v", got)
	}
	if got := Load(home, "staging", "101"); got != (State{}) {
		t.Fatalf("another server inherited state: %+v", got)
	}
	if mode := fileMode(t, FilePath(home, "prod", "101")); mode != 0o600 {
		t.Fatalf("state mode = %o, want 600", mode)
	}
}

func TestCorruptStateFailsOpenToZero(t *testing.T) {
	home := t.TempDir()
	path := FilePath(home, "prod", "101")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load(home, "prod", "101"); got != (State{}) {
		t.Fatalf("corrupt state = %+v, want zero", got)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
