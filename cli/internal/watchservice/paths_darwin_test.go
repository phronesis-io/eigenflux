package watchservice

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateLaunchPath(t *testing.T) {
	home := t.TempDir()
	for _, folder := range []string{"Desktop", "Documents", "Downloads", "Library/Mobile Documents"} {
		t.Run(folder, func(t *testing.T) {
			protected := filepath.Join(home, folder, "watch.log")
			if err := validateLaunchPath(home, "StandardOutPath", protected); !errors.Is(err, ErrProtectedLaunchPath) {
				t.Fatalf("expected protected path error, got %v", err)
			}
			sibling := filepath.Join(home, folder+"-logs", "watch.log")
			if err := validateLaunchPath(home, "StandardOutPath", sibling); err != nil {
				t.Fatalf("rejected sibling path: %v", err)
			}
		})
	}
	if err := validateLaunchPath(home, "StandardOutPath", "watch.log"); err == nil {
		t.Fatal("accepted relative log path")
	}
	dangling := filepath.Join(home, "logs")
	if err := os.Symlink(filepath.Join(home, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	if err := validateLaunchPath(home, "StandardOutPath", filepath.Join(dangling, "watch.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing symlink target error, got %v", err)
	}
}
