package skills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncReadAccessFailureDoesNotCreateLock(t *testing.T) {
	for _, scope := range []string{"parent", "directory", "manifest", "skill", "journal", "rollback"} {
		t.Run(scope, func(t *testing.T) {
			opts, _, _ := installedSyncFixture(t)
			parent := filepath.Dir(opts.Into)
			path := parent
			directory := true
			switch scope {
			case "directory":
				path = opts.Into
			case "manifest":
				path, directory = filepath.Join(opts.Into, ManifestFileName), false
			case "skill":
				path, directory = filepath.Join(opts.Into, "ef-broadcast", "SKILL.md"), false
			case "journal", "rollback":
				old := opts.Into + oldSuffix
				if err := copyDir(opts.Into, old); err != nil {
					t.Fatal(err)
				}
				if err := writeJournal(opts.Into+journalSuffix, old); err != nil {
					t.Fatal(err)
				}
				path = old
				if scope == "journal" {
					path, directory = opts.Into+journalSuffix, false
				}
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0)
			if directory {
				mode = 0300 // write/search allowed; reading denied
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, info.Mode().Perm()) })
			if directory {
				_, err = os.ReadDir(path)
			} else {
				_, err = os.ReadFile(path)
			}
			if err == nil {
				t.Skip("filesystem does not enforce read permissions")
			}
			if !errors.Is(err, os.ErrPermission) {
				t.Fatal(err)
			}
			before, err := os.Stat(parent)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := beginSyncWrite(opts.Into, parent)
			if lock != nil {
				lock.Release()
				t.Fatal("read access failure acquired lock")
			}
			if !errors.Is(err, os.ErrPermission) || !errors.Is(err, errSkillsPermissionRequired) {
				t.Fatalf("expected actionable read permission error, got %v", err)
			}
			after, err := os.Stat(parent)
			if err != nil || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("preflight modified parent (created/removed lock): %v", err)
			}
			if _, err := os.Lstat(filepath.Join(parent, lockFileName)); !os.IsNotExist(err) {
				t.Fatalf("preflight left a lock: %v", err)
			}
		})
	}
}

func TestSyncReadAccessAllowsMissingFirstInstall(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "missing", "parent")
	real := filepath.Join(parent, "skills")
	if err := checkSyncReadAccess(real, parent); err != nil {
		t.Fatalf("first-install preflight: %v", err)
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("preflight created parent: %v", err)
	}
	lock, err := beginSyncWrite(real, parent)
	if err != nil {
		t.Fatalf("first-install lock: %v", err)
	}
	lock.Release()
}

func TestSyncReadDenialIsNotHiddenByLocalEdits(t *testing.T) {
	src := stageSkills(t, map[string]map[string]string{
		"ef-broadcast": {"SKILL.md": "broadcast"},
		"ef-profile":   {"SKILL.md": "profile"},
	})
	server, _ := serveBundleAtSequence(t, "1.0.0", src, []string{"ef-broadcast", "ef-profile"}, 100)
	opts := syncOpts(filepath.Join(t.TempDir(), "skills"), "1.0.0", server.URL, nil)
	if _, err := Sync(opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.Into, "ef-broadcast", "SKILL.md"), []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(opts.Into, "ef-profile", "SKILL.md")
	if err := os.Chmod(blocked, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0600) })
	if _, err := os.ReadFile(blocked); err == nil {
		t.Skip("filesystem does not enforce read permissions")
	}
	before, _ := os.Stat(filepath.Dir(opts.Into))
	res, err := Sync(opts)
	if res != nil || !errors.Is(err, errSkillsPermissionRequired) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("local edit masked read denial: %+v, %v", res, err)
	}
	after, _ := os.Stat(filepath.Dir(opts.Into))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("read denial created or removed a lock")
	}
}
