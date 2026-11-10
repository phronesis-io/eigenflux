package selfupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func cleanupPackageFixture(t *testing.T, root, version string, key ed25519.PrivateKey) string {
	t.Helper()
	dir := filepath.Join(root+".versions", version)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	entries := []packageEntry{{name: versionExecutableName(), content: "signed CLI " + version, mode: 0755}}
	if runtime.GOOS == "darwin" {
		binary, err := os.ReadFile("/usr/bin/true")
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, realCLITestHelper(t, t.TempDir(), "1.0.10", binary)...)
	}
	archive := testPackage(t, entries...)
	if err := ExtractPackage(bytes.NewReader(archive), int64(len(archive)), dir); err != nil {
		t.Fatal(err)
	}
	hash := func(data []byte) Artifact {
		sum := sha256.Sum256(data)
		return Artifact{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	}
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	legacy := Manifest{Version: version, Artifacts: map[string]Artifact{name: hash([]byte(entries[0].content))}}
	if err := Sign(&legacy, key); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(dir, "release.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Version: version, NotifierVersion: "1.0.10", Artifacts: legacy.Artifacts, Packages: map[string]Artifact{name + ".zip": hash(archive)}}
	if err := Sign(&m, key); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(m)
	for name, data := range map[string][]byte{".package-release.json": data, packageArchive: archive} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func cleanupRetryFixture(t *testing.T) (string, ed25519.PublicKey, *InstallationCleanup) {
	t.Helper()
	root, public, key := installationFixture(t)
	cleanupPackageFixture(t, root, "1.0.1", key)
	cleanupPackageFixture(t, root, "1.0.2", key)
	if err := ActivateInstallation(root, "1.0.2", public); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanInstallationCleanup(context.Background(), root, public)
	if err != nil {
		t.Fatal(err)
	}
	return root, public, plan
}

func TestInstallationCleanupResumesEveryDeletionBoundary(t *testing.T) {
	root, public, plan := cleanupRetryFixture(t)
	pointer := root + ".current.json"
	verify := func() {
		t.Helper()
		live, err := RevalidateInstallationCleanup(context.Background(), root, public, plan)
		if err != nil {
			t.Fatal(err)
		}
		for file := range live.Files {
			if _, err := os.Lstat(file); err != nil {
				t.Fatalf("resume included absent path %s: %v", file, err)
			}
		}
	}
	remove := func(file string) {
		t.Helper()
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		verify()
	}
	verify()
	metadataStarted := false
	for _, file := range plan.RemovalFiles(root) {
		if file == pointer {
			continue
		}
		metadata := filepath.Dir(filepath.Dir(file)) == root+".versions" && filepath.Base(file) != versionExecutableName()
		if metadata && !metadataStarted {
			for _, dir := range plan.Directories {
				if dir != root+".versions" && filepath.Dir(dir) != root+".versions" {
					remove(dir)
				}
			}
			metadataStarted = true
		}
		if !metadata && metadataStarted {
			t.Fatal("payload follows authentication metadata")
		}
		remove(file)
		if _, err := PlanInstallationCleanup(context.Background(), root, public); err == nil {
			t.Fatal("initial planning accepted partially removed package")
		}
	}
	for _, dir := range plan.Directories {
		remove(dir)
	}
	remove(pointer)
	live, err := RevalidateInstallationCleanup(context.Background(), root, public, plan)
	if err != nil || len(live.Files) != 0 || len(live.Directories) != 0 {
		t.Fatalf("completed cleanup cannot resume: %v %+v", err, live)
	}
}

func TestInstallationCleanupRetryRejectsChangedOrForgedContent(t *testing.T) {
	for _, scenario := range []string{"foreign_file", "foreign_directory", "tampered_payload", "executable_mode", "tampered_archive", "tampered_manifest", "linked_file", "linked_version", "linked_versions", "pointer", "forged_hash", "forged_path", "forged_directory", "forged_proof", "archive_missing_payload", "archive_missing_foreign_directory", "archive_missing_forged_metadata", "outside_path"} {
		t.Run(scenario, func(t *testing.T) {
			root, public, plan := cleanupRetryFixture(t)
			dir := filepath.Join(root+".versions", "1.0.1")
			payload := filepath.Join(dir, versionExecutableName())
			// The first successful removal models a Windows retry after a later
			// executable was locked. Strict planning must not be used on resume.
			if err := os.Remove(filepath.Join(root+".versions", "1.0.2", versionExecutableName())); err != nil {
				t.Fatal(err)
			}
			write := func(file string, data []byte) {
				t.Helper()
				if err := os.WriteFile(file, data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "foreign_file":
				write(filepath.Join(dir, "foreign"), []byte("preserve"))
			case "foreign_directory", "forged_directory":
				foreign := filepath.Join(dir, "foreign")
				if err := os.Mkdir(foreign, 0700); err != nil {
					t.Fatal(err)
				}
				if scenario == "forged_directory" {
					plan.Directories = append(plan.Directories, foreign)
				}
			case "tampered_payload":
				write(payload, []byte("changed"))
			case "executable_mode":
				if runtime.GOOS == "windows" {
					t.Skip("executable modes do not apply on Windows")
				}
				if err := os.Chmod(payload, 0600); err != nil {
					t.Fatal(err)
				}
			case "tampered_archive":
				write(filepath.Join(dir, packageArchive), []byte("changed"))
			case "tampered_manifest":
				write(filepath.Join(dir, "release.json"), []byte("changed"))
			case "linked_file":
				if err := os.Remove(payload); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root, payload); err != nil {
					t.Skip(err)
				}
			case "linked_version", "linked_versions":
				target := dir
				if scenario == "linked_versions" {
					target = root + ".versions"
				}
				if err := os.Rename(target, target+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target+".saved", target); err != nil {
					t.Skip(err)
				}
			case "pointer":
				write(root+".current.json", []byte(`{"version":"1.0.1"}`))
			case "forged_hash":
				plan.Files[payload] = strings.Repeat("0", 64)
			case "forged_path":
				foreign := filepath.Join(dir, "foreign")
				write(foreign, []byte("preserve"))
				plan.Files[foreign] = cleanupDigest([]byte("preserve"))
			case "forged_proof":
				proof := plan.Packages["1.0.1"]
				proof.Manifest = bytes.ReplaceAll(proof.Manifest, []byte("1.0.1"), []byte("2.0.0"))
				plan.Packages["1.0.1"] = proof
			case "archive_missing_payload", "archive_missing_foreign_directory", "archive_missing_forged_metadata":
				if scenario != "archive_missing_payload" {
					// Reach the legitimate post-payload stage before attempting to
					// forge metadata or empty-directory ownership without the ZIP.
					for file := range plan.Files {
						if !strings.HasPrefix(file, dir+string(filepath.Separator)) {
							continue
						}
						if filepath.Dir(file) == dir && (filepath.Base(file) == "release.json" || filepath.Base(file) == ".package-release.json" || filepath.Base(file) == packageArchive) {
							continue
						}
						if err := os.Remove(file); err != nil {
							t.Fatal(err)
						}
					}
					for _, directory := range plan.Directories {
						if strings.HasPrefix(directory, dir+string(filepath.Separator)) {
							if err := os.Remove(directory); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				if err := os.Remove(filepath.Join(dir, packageArchive)); err != nil {
					t.Fatal(err)
				}
				if scenario == "archive_missing_foreign_directory" {
					foreign := filepath.Join(dir, "foreign")
					if err := os.Mkdir(foreign, 0700); err != nil {
						t.Fatal(err)
					}
					plan.Directories = append(plan.Directories, foreign)
				}
				if scenario == "archive_missing_forged_metadata" {
					file := filepath.Join(dir, "release.json")
					write(file, []byte("foreign"))
					plan.Files[file] = cleanupDigest([]byte("foreign"))
				}
			case "outside_path":
				plan.Files[filepath.Join(filepath.Dir(root), "outside")] = strings.Repeat("0", 64)
			}
			if _, err := RevalidateInstallationCleanup(context.Background(), root, public, plan); err == nil {
				t.Fatal("retry accepted unverified content")
			}
			if _, err := os.Lstat(root); err != nil {
				t.Fatal("validation mutated stable entry")
			}
		})
	}
}

func TestInstallationCleanupNilReceiptRejectsNewPackages(t *testing.T) {
	root, public, key := installationFixture(t)
	if plan, err := RevalidateInstallationCleanup(context.Background(), root, public, nil); err != nil || plan != nil {
		t.Fatalf("bare install changed: %v", err)
	}
	cleanupPackageFixture(t, root, "1.0.1", key)
	if _, err := RevalidateInstallationCleanup(context.Background(), root, public, nil); err == nil {
		t.Fatal("new package accepted without initial receipt")
	}
}

func TestInstallationCleanupAuthenticatesAllVersionsAndPointer(t *testing.T) {
	root, pub, key := installationFixture(t)
	cleanupPackageFixture(t, root, "1.0.1", key)
	cleanupPackageFixture(t, root, "1.0.2", key)
	if err := ActivateInstallation(root, "1.0.2", pub); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanInstallationCleanup(context.Background(), root, pub)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Files[root+".current.json"] == "" || plan.Files[filepath.Join(root+".versions", "1.0.1", versionExecutableName())] == "" {
		t.Fatal("old release or activation pointer omitted")
	}
	if plan.Files[root] != "" || plan.Files[root+".activation.lock"] != "" {
		t.Fatal("cleanup crossed into the stable entry or a shared lock")
	}
	if plan.Directories[len(plan.Directories)-1] != root+".versions" {
		t.Fatal("directories are not ordered for non-recursive removal")
	}
	for name, digest := range plan.Files {
		if actual, err := packageFileHash(name); err != nil || digest != actual {
			t.Fatal("uninstall snapshot does not pin actual files")
		}
	}
}

func TestInstallationCleanupPreservesUnknownOrChangedContent(t *testing.T) {
	for _, scenario := range []string{"foreign_file", "foreign_directory", "tampered_cli", "tampered_archive", "untrusted_manifest", "linked_file", "linked_versions", "changed_pointer"} {
		t.Run(scenario, func(t *testing.T) {
			root, pub, key := installationFixture(t)
			dir := cleanupPackageFixture(t, root, "1.0.1", key)
			if err := ActivateInstallation(root, "1.0.1", pub); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "foreign_file":
				_ = os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("foreign"), 0600)
			case "foreign_directory":
				_ = os.Mkdir(filepath.Join(root+".versions", "unknown"), 0700)
			case "tampered_cli":
				_ = os.WriteFile(filepath.Join(dir, versionExecutableName()), []byte("changed"), 0700)
			case "tampered_archive":
				_ = os.WriteFile(filepath.Join(dir, packageArchive), []byte("changed"), 0600)
			case "untrusted_manifest":
				_, pub, _ = installationFixture(t)
			case "linked_file":
				file := filepath.Join(dir, versionExecutableName())
				_ = os.Remove(file)
				if err := os.Symlink(root, file); err != nil {
					t.Skip(err)
				}
			case "linked_versions":
				_ = os.Rename(root+".versions", root+".saved")
				if err := os.Symlink(root+".saved", root+".versions"); err != nil {
					t.Skip(err)
				}
			case "changed_pointer":
				_ = os.WriteFile(root+".current.json", []byte(`{"version":"2.0.0"}`), 0600)
			}
			if _, err := PlanInstallationCleanup(context.Background(), root, pub); err == nil {
				t.Fatal("unverified cleanup accepted")
			}
			if _, err := os.Stat(root); err != nil {
				t.Fatal("verification mutated the installation")
			}
		})
	}
}
