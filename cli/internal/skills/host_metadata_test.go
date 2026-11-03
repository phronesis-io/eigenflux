package skills

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundlePreservationFailureCanRetryAfterReadAccessGranted(t *testing.T) {
	for _, directory := range []string{".a-readonly", "a-readonly"} {
		t.Run(directory, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "skills")
			source := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "old"}})
			opts := SyncOptions{Into: target, BundleDir: source, CLIVersion: "1.0.0"}
			if _, err := InstallFromBundle(opts); err != nil {
				t.Fatal(err)
			}
			readonly := filepath.Join(target, directory)
			if err := os.Mkdir(readonly, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(readonly, "state"), []byte("host state"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(readonly, 0500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, root := range []string{target, target + newSuffix, target + oldSuffix} {
					_ = os.Chmod(filepath.Join(root, directory), 0700)
				}
			})
			blocked := filepath.Join(target, "z", "file")
			if err := os.Mkdir(filepath.Dir(blocked), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(blocked, []byte("host data"), 0000); err != nil {
				t.Fatal(err)
			}
			if _, err := os.ReadFile(blocked); err == nil {
				t.Skip("filesystem does not enforce read permissions")
			}
			if err := os.WriteFile(filepath.Join(source, "ef-broadcast", "SKILL.md"), []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(target, ManifestFileName))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := InstallFromBundle(opts); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("expected read refusal: %v", err)
			}
			after, err := os.ReadFile(filepath.Join(target, ManifestFileName))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed preservation changed active manifest", err)
			}
			if info, err := os.Stat(readonly); err != nil || info.Mode().Perm() != 0500 {
				t.Fatal("cleanup changed active host permissions", err)
			}
			if _, err := os.Stat(target + newSuffix); !os.IsNotExist(err) {
				t.Fatalf("failed preservation left staging debris: %v", err)
			}
			if err := os.Chmod(blocked, 0600); err != nil {
				t.Fatal(err)
			}
			result, err := InstallFromBundle(opts)
			if err != nil || result == nil || !result.Atomic {
				t.Fatalf("retry after read grant failed: %+v %v", result, err)
			}
			if data, err := os.ReadFile(filepath.Join(target, "ef-broadcast", "SKILL.md")); err != nil || string(data) != "new" {
				t.Fatal("retry did not update Skill", err)
			}
			if data, err := os.ReadFile(filepath.Join(readonly, "state")); err != nil || string(data) != "host state" {
				t.Fatal("retry lost host state", err)
			}
			if data, err := os.ReadFile(blocked); err != nil || string(data) != "host data" {
				t.Fatal("retry lost previously unreadable host data", err)
			}
			if info, err := os.Stat(readonly); err != nil || info.Mode().Perm() != 0500 {
				t.Fatal("retry changed host permissions", err)
			}
			if _, err := os.Stat(target + oldSuffix); !os.IsNotExist(err) {
				t.Fatalf("successful retry left read-only backup: %v", err)
			}
		})
	}
}

func TestInstallationReplacesReadOnlyStagingWithoutFollowingLinks(t *testing.T) {
	for _, mode := range []string{"sync", "bundle"} {
		t.Run(mode, func(t *testing.T) {
			opts, _, _ := installedSyncFixture(t)
			external := t.TempDir()
			if err := os.WriteFile(filepath.Join(external, "keep"), []byte("external"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(external, 0500); err != nil {
				t.Fatal(err)
			}
			externalBefore, err := os.Stat(external)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(external, 0700) })
			stage := opts.Into + newSuffix
			readonly := filepath.Join(stage, ".readonly")
			if err := os.MkdirAll(readonly, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(readonly, "partial"), []byte("partial"), 0600); err != nil {
				t.Fatal(err)
			}
			linked := os.Symlink(external, filepath.Join(readonly, "outside")) == nil
			if err := os.Chmod(readonly, 0500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(readonly, 0700) })
			source := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "updated"}})
			var result *SyncResult
			err = nil
			if mode == "sync" {
				server, _ := serveBundleAtSequence(t, "1.0.0", source, []string{"ef-broadcast"}, 101)
				opts.CDNBase = server.URL
				result, err = Sync(opts)
			} else {
				opts.BundleDir = source
				result, err = InstallFromBundle(opts)
			}
			if err != nil || result == nil || !result.Atomic {
				t.Fatalf("stale stage prevented installation: %+v %v", result, err)
			}
			if _, err := os.Stat(stage); !os.IsNotExist(err) {
				t.Fatal("stale stage remains", err)
			}
			if linked {
				if data, err := os.ReadFile(filepath.Join(external, "keep")); err != nil || string(data) != "external" {
					t.Fatal("cleanup followed staging symlink", err)
				}
				info, err := os.Stat(external)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != externalBefore.Mode().Perm() {
					t.Fatal("cleanup changed external directory permissions")
				}
			}
		})
	}
}

func TestInstallationPreservesHostMetadata(t *testing.T) {
	for _, mode := range []string{"sync-first", "sync-update", "bundle-first", "bundle-update", "offline-first"} {
		t.Run(mode, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "skills")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			updating := mode == "sync-update" || mode == "bundle-update"
			if updating {
				old := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "old"}, "ef-profile": {"SKILL.md": "retired"}})
				if _, err := InstallFromBundle(SyncOptions{Into: target, BundleDir: old, CLIVersion: "1.0.0"}); err != nil {
					t.Fatal(err)
				}
			}
			files := map[string]string{
				".curator_state": "private curator state\n", ".bundled_manifest": "native bundle metadata\n",
				".hub/registry.json": "{\"host\":\"hermes\"}\n", ".hub/.nested/.private": "nested metadata",
				"host-index.json": "visible root file", "custom/SKILL.md": "custom skill", "custom/.private": "private custom data",
				".hub/.DS_Store": "host bookkeeping", ".hub/helper": "executable helper",
			}
			modes := map[string]os.FileMode{}
			for name, content := range files {
				path := filepath.Join(target, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				if name == ".hub/helper" {
					if err := os.Chmod(path, 0750); err != nil {
						t.Fatal(err)
					}
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				modes[name] = info.Mode().Perm()
			}
			dirModes := map[string]os.FileMode{}
			for name, mode := range map[string]os.FileMode{".hub": 0750, "custom": 0710} {
				path := filepath.Join(target, name)
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				dirModes[name] = info.Mode().Perm()
			}
			// EigenFlux's own provisional state and interrupted manifest writes
			// must not be copied back over the new installation metadata.
			for _, name := range []string{StaleMarkerName, ".ef-manifest-interrupted.tmp"} {
				if err := os.WriteFile(filepath.Join(target, name), []byte("obsolete"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			source := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "new"}})
			opts := SyncOptions{Into: target, BundleDir: source, CLIVersion: "1.0.0"}
			var result *SyncResult
			var err error
			switch mode {
			case "bundle-first", "bundle-update":
				result, err = InstallFromBundle(opts)
			case "offline-first":
				opts.CDNBase, opts.FromBundle = "http://127.0.0.1:1", true
				result, err = Sync(opts)
			default:
				server, _ := serveBundleAtSequence(t, "1.0.0", source, []string{"ef-broadcast"}, 101)
				opts.CDNBase = server.URL
				result, err = Sync(opts)
			}
			if err != nil || result == nil || !result.Atomic {
				t.Fatalf("installation failed: %+v %v", result, err)
			}
			for name, content := range files {
				path := filepath.Join(target, name)
				data, err := os.ReadFile(path)
				if err != nil || string(data) != content {
					t.Fatalf("host file %s was lost or changed: %q %v", name, data, err)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != modes[name] {
					t.Fatalf("host file permissions changed: %s %v %v", name, info, err)
				}
			}
			for name, mode := range dirModes {
				info, err := os.Stat(filepath.Join(target, name))
				if err != nil || info.Mode().Perm() != mode {
					t.Fatalf("host directory permissions changed: %s %v %v", name, info, err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(target, "ef-broadcast", "SKILL.md")); err != nil || string(data) != "new" {
				t.Fatal("official Skill did not update", err)
			}
			if _, err := os.Stat(filepath.Join(target, "ef-profile")); !os.IsNotExist(err) {
				t.Fatal("retired managed Skill was retained", err)
			}
			manifest, err := ReadLocalManifest(target)
			if err != nil || manifest == nil || len(manifest.Skills) != 1 {
				t.Fatalf("new manifest replaced with host state: %+v %v", manifest, err)
			}
			if _, err := os.Stat(filepath.Join(target, ".ef-manifest-interrupted.tmp")); !os.IsNotExist(err) {
				t.Fatal("obsolete manifest temporary file retained", err)
			}
			_, staleErr := os.Stat(filepath.Join(target, StaleMarkerName))
			if mode == "offline-first" {
				if !result.Stale || staleErr != nil {
					t.Fatal("offline provisional marker missing", staleErr)
				}
			} else if !os.IsNotExist(staleErr) {
				t.Fatal("obsolete provisional marker retained", staleErr)
			}
		})
	}
}

func TestInstallationRejectsHostSpecialFileWithoutSwapping(t *testing.T) {
	for _, mode := range []string{"sync", "bundle"} {
		t.Run(mode, func(t *testing.T) {
			// Keep the Unix socket path below platform sockaddr length limits.
			root, err := os.MkdirTemp("", "ef-host-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			target := filepath.Join(root, "skills")
			source := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "old"}})
			if _, err := InstallFromBundle(SyncOptions{Into: target, BundleDir: source, CLIVersion: "1.0.0"}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(target, ManifestFileName))
			if err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(target, ".socket")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Skipf("Unix sockets unavailable: %v", err)
			}
			defer listener.Close()
			source = stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "new"}})
			opts := SyncOptions{Into: target, BundleDir: source, CLIVersion: "1.0.0"}
			if mode == "sync" {
				server, _ := serveBundleAtSequence(t, "1.0.0", source, []string{"ef-broadcast"}, 101)
				opts.CDNBase = server.URL
				_, err = Sync(opts)
			} else {
				_, err = InstallFromBundle(opts)
			}
			if err == nil || !strings.Contains(err.Error(), "unsupported host metadata file type") {
				t.Fatalf("special file accepted: %v", err)
			}
			after, err := os.ReadFile(filepath.Join(target, ManifestFileName))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed preservation replaced the active manifest", err)
			}
			if content, err := os.ReadFile(filepath.Join(target, "ef-broadcast", "SKILL.md")); err != nil || string(content) != "old" {
				t.Fatal("failed preservation replaced the active Skill", err)
			}
			if info, err := os.Lstat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
				t.Fatal("host socket was lost", err)
			}
		})
	}
}

func TestSyncPreservesHostSymlinksWithoutFollowing(t *testing.T) {
	opts, _, _ := installedSyncFixture(t)
	external := filepath.Join(t.TempDir(), "private-target")
	if err := os.WriteFile(external, []byte("external file"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(external, 0600) })
	if err := os.Symlink(external, filepath.Join(opts.Into, ".host-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(opts.Into, ".hub"), 0700); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("..", ".host-link")
	if err := os.Symlink(relative, filepath.Join(opts.Into, ".hub", "nested-link")); err != nil {
		t.Fatal(err)
	}
	source := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "new"}})
	server, _ := serveBundleAtSequence(t, "1.0.0", source, []string{"ef-broadcast"}, 101)
	opts.CDNBase = server.URL
	if result, err := Sync(opts); err != nil || !result.VerifiedManifest {
		t.Fatalf("sync failed: %+v %v", result, err)
	}
	for name, want := range map[string]string{".host-link": external, ".hub/nested-link": relative} {
		if got, err := os.Readlink(filepath.Join(opts.Into, name)); err != nil || got != want {
			t.Fatalf("symlink changed: %s %q %v", name, got, err)
		}
	}
}
