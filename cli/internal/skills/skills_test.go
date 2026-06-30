package skills

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// stageSkills writes skills (name -> file -> content) under a temp dir and
// returns the dir.
func stageSkills(t *testing.T, skills map[string]map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, files := range skills {
		for fn, content := range files {
			p := filepath.Join(dir, name, fn)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

// tarGzDir builds a deterministic-ish gzip tar of the named subdirs of src.
func tarGzDir(t *testing.T, src string, names []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		root := filepath.Join(src, name)
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			hdr, _ := tar.FileInfoHeader(info, "")
			hdr.Name = filepath.ToSlash(rel)
			if info.IsDir() {
				hdr.Name += "/"
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			data, _ := os.ReadFile(p)
			_, err = tw.Write(data)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// serveBundle stands up an httptest server exposing cli/<ver>/ and cli/latest/.
func serveBundle(t *testing.T, version string, src string, names []string) (*httptest.Server, *Manifest) {
	t.Helper()
	m, err := GenerateManifest(src, version, names, 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	tarGz := tarGzDir(t, src, names)
	sum := sha256.Sum256(tarGz)
	m.TarSHA256 = hex.EncodeToString(sum[:])
	manBytes, _ := json.Marshal(m)

	mux := http.NewServeMux()
	for _, prefix := range []string{"/cli/" + version, "/cli/latest"} {
		p := prefix
		mux.HandleFunc(p+"/"+RemoteManifest, func(w http.ResponseWriter, r *http.Request) { w.Write(manBytes) })
		mux.HandleFunc(p+"/"+TarName, func(w http.ResponseWriter, r *http.Request) { w.Write(tarGz) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, m
}

func syncOpts(into, version, cdn string, names []string) SyncOptions {
	return SyncOptions{Into: into, CLIVersion: version, CDNBase: cdn, Allowlist: names}
}

func TestDirSHA256Deterministic(t *testing.T) {
	a := stageSkills(t, map[string]map[string]string{
		"ef-broadcast": {"SKILL.md": "hi", "references/x.md": "x"},
	})
	h1, err := dirSHA256(filepath.Join(a, "ef-broadcast"))
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := dirSHA256(filepath.Join(a, "ef-broadcast"))
	if h1 != h2 {
		t.Fatalf("non-deterministic: %s != %s", h1, h2)
	}
	// .DS_Store must not change the hash.
	os.WriteFile(filepath.Join(a, "ef-broadcast", ".DS_Store"), []byte("junk"), 0o644)
	h3, _ := dirSHA256(filepath.Join(a, "ef-broadcast"))
	if h3 != h1 {
		t.Fatalf(".DS_Store changed hash: %s != %s", h3, h1)
	}
}

func TestSyncInstallUpdateReconcilePreserve(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "skills")

	// v1: ef-broadcast + ef-profile
	src1 := stageSkills(t, map[string]map[string]string{
		"ef-broadcast": {"SKILL.md": "b1"},
		"ef-profile":   {"SKILL.md": "p1"},
	})
	names1 := []string{"ef-broadcast", "ef-profile"}
	srv1, _ := serveBundle(t, "0.0.16", src1, names1)

	// A user-placed third-party skill that must survive every sync.
	if err := os.MkdirAll(filepath.Join(dst, "my-custom"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dst, "my-custom", "SKILL.md"), []byte("mine"), 0o644)

	res, err := Sync(syncOpts(dst, "0.0.16", srv1.URL, names1))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Source != "cli/0.0.16" {
		t.Fatalf("source=%s", res.Source)
	}
	for _, n := range []string{"ef-broadcast", "ef-profile", "my-custom"} {
		if !dirExists(filepath.Join(dst, n)) {
			t.Fatalf("missing %s after install", n)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, ManifestFileName)); err != nil {
		t.Fatalf("no local manifest: %v", err)
	}

	// v2: ef-broadcast changed, ef-profile removed, ef-trading added.
	src2 := stageSkills(t, map[string]map[string]string{
		"ef-broadcast": {"SKILL.md": "b2-changed"},
		"ef-trading":   {"SKILL.md": "t1"},
	})
	names2 := []string{"ef-broadcast", "ef-trading"}
	srv2, _ := serveBundle(t, "0.0.17", src2, names2)

	res2, err := Sync(syncOpts(dst, "0.0.17", srv2.URL, names2))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !dirExists(filepath.Join(dst, "ef-trading")) {
		t.Fatal("ef-trading not installed on update")
	}
	if dirExists(filepath.Join(dst, "ef-profile")) {
		t.Fatal("ef-profile should have been reconciled away")
	}
	if !dirExists(filepath.Join(dst, "my-custom")) {
		t.Fatal("third-party my-custom was clobbered")
	}
	b, _ := os.ReadFile(filepath.Join(dst, "ef-broadcast", "SKILL.md"))
	if string(b) != "b2-changed" {
		t.Fatalf("ef-broadcast not updated: %q", b)
	}
	found := false
	for _, r := range res2.Removed {
		if r == "ef-profile" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ef-profile not reported removed: %v", res2.Removed)
	}
}

func TestSyncIfStaleSkipsNetwork(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "skills")
	src := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "b"}})
	names := []string{"ef-broadcast"}
	srv, _ := serveBundle(t, "0.0.16", src, names)
	if _, err := Sync(syncOpts(dst, "0.0.16", srv.URL, names)); err != nil {
		t.Fatal(err)
	}
	// Point at a dead server; --if-stale with matching version must not hit it.
	o := syncOpts(dst, "0.0.16", "http://127.0.0.1:1", names)
	o.IfStale = true
	res, err := Sync(o)
	if err != nil {
		t.Fatalf("if-stale should be offline-safe: %v", err)
	}
	if res.Source != "local" {
		t.Fatalf("expected local short-circuit, got %s", res.Source)
	}
}

func TestSyncKeepsLocalOnNetworkFailure(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "skills")
	src := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "b"}})
	names := []string{"ef-broadcast"}
	srv, _ := serveBundle(t, "0.0.16", src, names)
	if _, err := Sync(syncOpts(dst, "0.0.16", srv.URL, names)); err != nil {
		t.Fatal(err)
	}
	// New version, but server unreachable: must keep local, not wipe.
	res, err := Sync(syncOpts(dst, "0.0.17", "http://127.0.0.1:1", names))
	if err == nil {
		// non-quiet returns the error; quiet would swallow. Either way dst intact.
		t.Log("returned err is acceptable")
	}
	_ = res
	if !dirExists(filepath.Join(dst, "ef-broadcast")) {
		t.Fatal("local skills wiped on network failure")
	}
}

func TestReconcileZombiesManagedByLock(t *testing.T) {
	// A local manifest NOT managed by us must never yield removals.
	local := &Manifest{ManagedBy: "someone-else", Skills: []SkillEntry{{Name: "x"}}}
	remote := &Manifest{ManagedBy: ManagedByValue, Skills: []SkillEntry{{Name: "y"}}}
	if got := reconcileZombies(local, remote); got != nil {
		t.Fatalf("foreign manifest should not be reconciled, got %v", got)
	}
	managed := &Manifest{ManagedBy: ManagedByValue, Skills: []SkillEntry{{Name: "x"}, {Name: "y"}}}
	got := reconcileZombies(managed, remote)
	if len(got) != 1 || got[0] != "x" {
		t.Fatalf("expected [x] removed, got %v", got)
	}
}

func TestVerifyManifestRejectsExtraDir(t *testing.T) {
	newDir := stageSkills(t, map[string]map[string]string{
		"ef-broadcast": {"SKILL.md": "b"},
		"ef-localdev":  {"SKILL.md": "evil"}, // not in manifest -> must be rejected
	})
	sum, _ := dirSHA256(filepath.Join(newDir, "ef-broadcast"))
	m := &Manifest{Skills: []SkillEntry{{Name: "ef-broadcast", SHA256: sum}}}
	if err := verifyManifest(newDir, m); err == nil {
		t.Fatal("verifyManifest must reject an extra (unlisted) dir")
	}
}

func TestAcquireLockExclusive(t *testing.T) {
	parent := t.TempDir()
	l1, ok1, err := acquireLock(parent)
	if err != nil || !ok1 {
		t.Fatalf("first lock failed: ok=%v err=%v", ok1, err)
	}
	_, ok2, _ := acquireLock(parent)
	if ok2 {
		t.Fatal("second lock should fail while first is held")
	}
	l1.Release()
	l3, ok3, _ := acquireLock(parent)
	if !ok3 {
		t.Fatal("lock should be acquirable after release")
	}
	l3.Release()
}

func TestRecoverInterruptedRollsBack(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "skills")
	old := real + oldSuffix
	// Simulate crash between A and B: real moved to old, journal points to old.
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "marker"), []byte("v1"), 0o644)
	writeJournal(real+journalSuffix, old)

	recoverInterrupted(real)

	if !dirExists(real) {
		t.Fatal("recover should have rolled old back to real")
	}
	if _, err := os.Stat(real + journalSuffix); !os.IsNotExist(err) {
		t.Fatal("journal should be cleared after recovery")
	}
}

func TestPreserveReportsUserEdit(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "skills")
	bundle := stageSkills(t, map[string]map[string]string{
		"ef-broadcast": {"SKILL.md": "b1"},
		"ef-profile":   {"SKILL.md": "p1"},
	})
	names := []string{"ef-broadcast", "ef-profile"}
	opts := SyncOptions{Into: dst, BundleDir: bundle, CLIVersion: "0.0.16", Allowlist: names}
	if _, err := InstallFromBundle(opts); err != nil {
		t.Fatal(err)
	}
	// User hand-edits one managed skill.
	if err := os.WriteFile(filepath.Join(dst, "ef-broadcast", "SKILL.md"), []byte("b1-EDITED"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := InstallFromBundle(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Preserved) != 1 || res.Preserved[0] != "ef-broadcast" {
		t.Fatalf("expected ef-broadcast reported preserved, got %v", res.Preserved)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "ef-broadcast", "SKILL.md"))
	if string(got) != "b1-EDITED" {
		t.Fatalf("user edit was clobbered: %q", got)
	}
}

func TestRecoverInterruptedNoJournalNoOp(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "skills")
	// Stale .ef-old debris with NO journal must not be restored.
	old := real + oldSuffix
	os.MkdirAll(old, 0o755)
	recoverInterrupted(real)
	if dirExists(real) {
		t.Fatal("must not restore stale old without a journal")
	}
}
