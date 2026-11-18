package watchservice

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateLaunchPath(t *testing.T) {
	home := t.TempDir()
	for _, folder := range []string{"Desktop", "Documents", "Downloads", "Library/Mobile Documents"} {
		t.Run(folder, func(t *testing.T) {
			protected := filepath.Join(home, folder, "watch.log")
			if err := (launchLogPaths{StandardOutPath: protected}).validate(home); err == nil || !strings.Contains(err.Error(), "privacy-protected") {
				t.Fatalf("expected protected path error, got %v", err)
			}
			sibling := filepath.Join(home, folder+"-logs", "watch.log")
			if err := (launchLogPaths{StandardOutPath: sibling}).validate(home); err != nil {
				t.Fatalf("rejected sibling path: %v", err)
			}
		})
	}
	if err := (launchLogPaths{StandardOutPath: "watch.log"}).validate(home); err != nil {
		t.Fatalf("rejected legacy relative log path: %v", err)
	}
}
