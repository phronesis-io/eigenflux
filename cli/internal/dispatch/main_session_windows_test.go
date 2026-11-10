package dispatch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMainSessionWindowsJunctionRoot(t *testing.T) {
	for _, scenario := range []string{"valid", "metadata mismatch", "missing target"} {
		t.Run(scenario, func(t *testing.T) {
			b := mainSessionFixture(t)
			home := b.Env["CODEX_HOME"]
			root := filepath.Join(home, "sessions")
			target := filepath.Join(t.TempDir(), "physical sessions")
			if err := os.Rename(root, target); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("cmd", "/c", "mklink", "/J", root, target).CombinedOutput(); err != nil {
				t.Fatalf("create junction: %v: %s", err, out)
			}
			// Remove the junction before TempDir cleanup touches its target.
			t.Cleanup(func() {
				if err := os.Remove(root); err != nil {
					t.Errorf("remove junction: %v", err)
				}
			})
			path := filepath.Join(target, "2026", "09", "24", "rollout-date-"+mainTestID+".jsonl")
			switch scenario {
			case "metadata mismatch":
				if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"another-thread"}}`+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing target":
				if err := os.RemoveAll(target); err != nil {
					t.Fatal(err)
				}
			}
			got, err := b.mainSessionPath()
			if scenario != "valid" {
				if !errors.Is(err, ErrMainSessionUnavailable) {
					t.Fatalf("accepted %s: %q, %v", scenario, got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			gotInfo, err := os.Stat(got)
			if err != nil || !os.SameFile(wantInfo, gotInfo) {
				t.Fatalf("wrong session file: %q, %v", got, err)
			}
			if b.Env["CODEX_HOME"] != home {
				t.Fatal("CODEX_HOME changed")
			}
			if err := b.ValidateMainSession(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
