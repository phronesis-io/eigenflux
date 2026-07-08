package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDueForAutoSync(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ttl := 24 * time.Hour
	cases := []struct {
		name string
		last int64
		want bool
	}{
		{"never attempted", 0, true},
		{"negative/garbage", -5, true},
		{"just now", now.Unix(), false},
		{"within ttl", now.Add(-1 * time.Hour).Unix(), false},
		{"exactly at ttl", now.Add(-24 * time.Hour).Unix(), true},
		{"past ttl", now.Add(-25 * time.Hour).Unix(), true},
		{"future (clock rewind)", now.Add(1 * time.Hour).Unix(), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DueForAutoSync(AutoSyncState{LastAttemptUnix: c.last}, ttl, now)
			if got != c.want {
				t.Fatalf("DueForAutoSync(last=%d) = %v, want %v", c.last, got, c.want)
			}
		})
	}
}

func TestAutoSyncStateRoundtrip(t *testing.T) {
	dir := t.TempDir()

	// Missing file => zero state, no error.
	if s := LoadAutoSyncState(dir); s != (AutoSyncState{}) {
		t.Fatalf("missing file: want zero, got %+v", s)
	}

	want := AutoSyncState{LastAttemptUnix: 12345, LastRevision: "rev-abc", LastResult: "ok"}
	if err := SaveAutoSyncState(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := LoadAutoSyncState(dir); got != want {
		t.Fatalf("roundtrip: want %+v, got %+v", want, got)
	}

	// Corrupt file => zero state, never an error (can only cost one extra sync).
	if err := os.WriteFile(filepath.Join(dir, AutoSyncStateFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := LoadAutoSyncState(dir); s != (AutoSyncState{}) {
		t.Fatalf("corrupt file: want zero, got %+v", s)
	}
}
