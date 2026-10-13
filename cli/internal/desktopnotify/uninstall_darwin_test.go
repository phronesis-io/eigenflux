package desktopnotify

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUninstallMacNotificationOwnership(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown_preserved", true: "owned_removed"}[trusted], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			directory := filepath.Join(home, "Library", "Application Support", "EigenFlux", "Notifications")
			app := filepath.Join(directory, appName)
			if err := os.MkdirAll(app, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(app, "fixture"), []byte("owned test bundle"), 0600); err != nil {
				t.Fatal(err)
			}
			if trusted {
				digest, _ := bundleDigest(app)
				if err := os.WriteFile(filepath.Join(directory, ".bundle-owner"), []byte(digest), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := Uninstall(context.Background())
			if trusted {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(app); !os.IsNotExist(err) {
					t.Fatal("owned bundle remains")
				}
			} else {
				if err == nil {
					t.Fatal("unknown ownership accepted")
				}
				if _, err := os.Stat(app); err != nil {
					t.Fatal("unknown bundle deleted")
				}
			}
		})
	}
}

func TestMacOwnershipMarkerDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, appName)
	if err := os.MkdirAll(app, 0700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "unrelated")
	if err := os.WriteFile(unrelated, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(unrelated, filepath.Join(dir, ".bundle-owner")); err != nil {
		t.Fatal(err)
	}
	if err := recordBundleOwner(app, "digest"); err == nil {
		t.Fatal("linked marker accepted")
	}
	if data, _ := os.ReadFile(unrelated); string(data) != "preserve" {
		t.Fatal("unrelated file overwritten")
	}
}
