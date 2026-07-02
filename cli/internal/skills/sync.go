package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sync pulls the latest skills bundle from R2 into the host's real skill-load
// directory using a crash-safe whole-directory atomic swap. See package doc for
// the guarantees. It never deletes skills it did not install (managed_by lock)
// and preserves unrelated/user-modified skill folders.
func Sync(opts SyncOptions) (*SyncResult, error) {
	real, parent, err := prepareDir(opts)
	if err != nil {
		return nil, softFail(opts, err)
	}

	lock, locked, err := acquireLock(parent)
	if err != nil {
		return nil, softFail(opts, err)
	}
	if !locked {
		// Another process is syncing; harmless to skip for startup hooks.
		return &SyncResult{SkillsDir: real, Source: "local"}, nil
	}
	defer lock.Release()

	// Crash recovery MUST run inside the lock so it can't race a live swap.
	recoverInterrupted(real)

	local, _ := ReadLocalManifest(real)

	// Step 1: fetch ONLY the small manifest (cheap). Freshness is decided by the
	// content `revision`, NOT the CLI version — so a skill edit ships via R2
	// (release-skills) without a CLI republish.
	remote, dirURL, source, ferr := fetchManifest(opts)
	if ferr != nil {
		if local != nil {
			r := &SyncResult{SkillsDir: real, Source: "local", CLIVersion: local.CLIVersion, NoNetwork: true}
			// A background freshness check (startup hook) must never error out on
			// a transient network failure — keep what we have silently.
			if opts.IfStale || opts.Quiet {
				return r, nil
			}
			return r, softFail(opts, ferr)
		}
		if opts.FromBundle && opts.BundleDir != "" {
			return bundleApply(opts, real, parent, local, true) // provisional
		}
		return nil, softFail(opts, ferr)
	}

	// Step 2: compatibility floor. A CLI older than the bundle requires must not
	// adopt it (the skills may reference newer CLI commands). Keep local + nudge.
	if !cliMeetsMin(opts.CLIVersion, remote.MinCLIVersion) {
		msg := fmt.Sprintf("skills need CLI >= %s (have %s) — upgrade the CLI", remote.MinCLIVersion, opts.CLIVersion)
		if local != nil {
			return keepLocal(real, local, msg), nil
		}
		return nil, softFail(opts, fmt.Errorf("skills sync: %s (run install.sh)", msg))
	}

	// Step 3: already current → skip the tarball download entirely.
	if local != nil && local.Revision != "" && local.Revision == remote.Revision && !staleMarkerPresent(real) {
		return &SyncResult{SkillsDir: real, Source: "local", CLIVersion: local.CLIVersion}, nil
	}

	// Step 4: revision changed → pull + verify + atomic swap.
	tarGz, terr := fetchTarball(opts, dirURL, remote.Revision)
	if terr != nil {
		if local != nil {
			return keepLocal(real, local, "tarball fetch failed"), nil
		}
		return nil, softFail(opts, terr)
	}
	if err := verifyTarSHA(tarGz, remote.TarSHA256); err != nil {
		if local != nil {
			return keepLocal(real, local, "checksum mismatch"), nil
		}
		return nil, softFail(opts, err)
	}

	newDir := real + newSuffix
	os.RemoveAll(newDir)
	if err := extractTarGz(tarGz, newDir, opts.allowlist()); err != nil {
		os.RemoveAll(newDir)
		if local != nil {
			return keepLocal(real, local, "bad tar"), nil
		}
		return nil, softFail(opts, err)
	}

	return applyStaged(opts, real, parent, newDir, local, remote, source, false)
}

// applyStaged finishes a sync once newDir is populated: verify, preserve
// unmanaged/user-modified folders, then atomically swap newDir into place.
func applyStaged(opts SyncOptions, real, parent, newDir string, local, remote *Manifest, source string, stale bool) (*SyncResult, error) {
	if err := verifyManifest(newDir, remote); err != nil {
		os.RemoveAll(newDir)
		if local != nil {
			return keepLocal(real, local, "verify failed: "+err.Error()), nil
		}
		return nil, softFail(opts, err)
	}

	preserved, err := preserveUnmanaged(real, newDir, local, remote)
	if err != nil {
		os.RemoveAll(newDir)
		return nil, softFail(opts, err)
	}

	removed := reconcileZombies(local, remote)

	remote.ManagedBy = ManagedByValue
	if err := WriteManifestAtomic(newDir, remote); err != nil {
		os.RemoveAll(newDir)
		return nil, softFail(opts, err)
	}
	if stale {
		_ = os.WriteFile(filepath.Join(newDir, StaleMarkerName), []byte("provisional\n"), manifestFilePerm)
	}
	fsyncDir(newDir)

	res, err := swapInPlace(opts, parent, real, newDir, remote, source, removed, stale)
	if err == nil && res != nil {
		res.Preserved = preserved
	}
	return res, err
}

// swapInPlace performs the rename×2 swap with a journal + fsync(parent) at each
// step so a power loss leaves a deterministic, recoverable state.
func swapInPlace(opts SyncOptions, parent, real, newDir string, remote *Manifest, source string, removed []string, stale bool) (*SyncResult, error) {
	oldDir := real + oldSuffix
	journal := real + journalSuffix
	result := &SyncResult{
		SkillsDir: real, Source: source, CLIVersion: remote.CLIVersion,
		Removed: removed, Stale: stale, Atomic: true,
	}

	realExists := dirExists(real)
	if realExists {
		os.RemoveAll(oldDir)
		if err := writeJournal(journal, oldDir); err != nil {
			os.RemoveAll(newDir)
			return nil, softFail(opts, err)
		}
		fsyncDir(parent)

		if err := os.Rename(real, oldDir); err != nil { // ── A ──
			os.Remove(journal)
			fsyncDir(parent)
			os.RemoveAll(newDir)
			return nil, softFail(opts, err)
		}
		fsyncDir(parent) // A durable: real -> oldDir
	} else {
		// Fresh install: no old version to preserve, but still journal so an
		// interrupted B is recoverable (old="" means "no rollback target").
		if err := writeJournal(journal, ""); err != nil {
			os.RemoveAll(newDir)
			return nil, softFail(opts, err)
		}
		fsyncDir(parent)
	}

	if err := os.Rename(newDir, real); err != nil { // ── B ──
		if old := readJournalOld(journal); old != "" {
			os.Rename(old, real) // compensating rollback to previous version
			fsyncDir(parent)
		}
		os.Remove(journal)
		fsyncDir(parent)
		os.RemoveAll(newDir)
		return nil, softFail(opts, err)
	}
	fsyncDir(parent) // B durable: newDir -> real

	os.Remove(journal) // ── C: journal gone == swap complete ──
	if realExists {
		os.RemoveAll(oldDir)
	}
	fsyncDir(parent)
	return result, nil
}

// recoverInterrupted heals a swap interrupted by a crash. Called inside the lock.
// It trusts only the journal (never a fuzzy prefix scan), so it can never
// silently restore a stale old version.
func recoverInterrupted(real string) {
	journal := real + journalSuffix
	old, exists := readJournal(journal)
	if !exists {
		return
	}
	parent := filepath.Dir(real)
	// real missing + old present  => crashed between A and B: roll old back.
	if !dirExists(real) && old != "" && dirExists(old) {
		os.Rename(old, real)
	}
	// Clean any half-built newDir and a leftover old slot, then drop the journal.
	os.RemoveAll(real + newSuffix)
	if old != "" && old != real {
		os.RemoveAll(old)
	}
	os.Remove(journal)
	fsyncDir(parent)
}

// verifyManifest checks (a) every manifest skill is present in newDir with a
// matching dirSHA256, and (b) newDir's top-level skill set equals the manifest
// set — the only real defense against an injected/foreign folder (e.g. a
// poisoned ef-localdev) riding along in the archive.
func verifyManifest(newDir string, remote *Manifest) error {
	want := remote.names()
	for name, sha := range want {
		sd := filepath.Join(newDir, name)
		if !dirExists(sd) {
			return fmt.Errorf("missing skill %q", name)
		}
		sum, err := dirSHA256(sd)
		if err != nil {
			return err
		}
		if sum != sha {
			return fmt.Errorf("sha mismatch for %q", name)
		}
	}
	entries, err := os.ReadDir(newDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, ok := want[e.Name()]; !ok {
			return fmt.Errorf("unexpected dir %q not in manifest", e.Name())
		}
	}
	return nil
}

// reconcileZombies returns the names of skills we previously managed that are no
// longer in the new manifest (for reporting). The managed_by lock means a dir we
// did not install is never considered — actual removal happens because the new
// dir simply does not contain it after the swap.
func reconcileZombies(local, remote *Manifest) []string {
	if local == nil || local.ManagedBy != ManagedByValue {
		return nil
	}
	keep := remote.names()
	var dead []string
	for _, s := range local.Skills {
		if _, ok := keep[s.Name]; !ok {
			dead = append(dead, s.Name)
		}
	}
	return dead
}

// preserveUnmanaged copies into newDir any existing top-level skill folder that
// must survive the swap: (1) folders not in the remote manifest (third-party /
// user-placed), including on first install where local==nil; (2) a managed skill
// the user has hand-edited (on-disk sha != our recorded sha) — we keep their
// edit rather than clobber it.
// preserveUnmanaged returns the names of managed skills whose pending update was
// skipped because the user hand-edited them (so callers can surface that the
// skill is stuck on a local fork). Third-party folders are preserved verbatim
// but not reported, since no update was ever due for them.
func preserveUnmanaged(real, newDir string, local, remote *Manifest) (skippedUpdate []string, err error) {
	inRemote := remote.names()
	recorded := map[string]string{}
	if local != nil && local.ManagedBy == ManagedByValue {
		for _, s := range local.Skills {
			recorded[s.Name] = s.SHA256
		}
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := e.Name()
		src := filepath.Join(real, name)
		dst := filepath.Join(newDir, name)
		if _, ok := inRemote[name]; !ok {
			// Not in the new manifest. If WE previously managed it, it is a
			// zombie being reconciled away — do not preserve. Otherwise it is a
			// third-party / user-placed folder and must survive verbatim.
			if _, wasManaged := recorded[name]; wasManaged {
				continue
			}
			if err := replaceCopy(src, dst); err != nil {
				return nil, err
			}
			continue
		}
		// In the managed set: keep user edits to a previously-managed skill,
		// and report that we skipped the update for it.
		if want, isManaged := recorded[name]; isManaged {
			cur, err := dirSHA256(src)
			if err == nil && cur != want {
				if err := replaceCopy(src, dst); err != nil {
					return nil, err
				}
				skippedUpdate = append(skippedUpdate, name)
			}
		}
		// else: clean managed skill or unknown same-name dir => let the new
		// version win (do not copy).
	}
	return skippedUpdate, nil
}

// replaceCopy copies src over dst (removing any staged version first).
func replaceCopy(src, dst string) error {
	os.RemoveAll(dst)
	return copyDir(src, dst)
}

// --- small helpers -------------------------------------------------------

func prepareDir(opts SyncOptions) (real, parent string, err error) {
	dst, err := ResolveSkillsDir(opts.Into, opts.Host)
	if err != nil {
		return "", "", err
	}
	real = dst
	if r, e := filepath.EvalSymlinks(dst); e == nil {
		real = r
	}
	parent = filepath.Dir(real)
	if err := os.MkdirAll(parent, dirPerm); err != nil {
		return "", "", err
	}
	return real, parent, nil
}

func staleMarkerPresent(dir string) bool {
	return fileExists(filepath.Join(dir, StaleMarkerName))
}

func keepLocal(real string, local *Manifest, reason string) *SyncResult {
	fmt.Fprintf(os.Stderr, "skills sync: keeping local copy (%s)\n", reason)
	return &SyncResult{SkillsDir: real, Source: "local", CLIVersion: local.CLIVersion}
}

// softFail swallows the error when --quiet is set (startup hooks must never
// block on a transient network failure), otherwise returns it.
func softFail(o SyncOptions, e error) error {
	if e == nil {
		return nil
	}
	if o.Quiet {
		fmt.Fprintln(os.Stderr, "skills sync:", e)
		return nil
	}
	return e
}

func fsyncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	f.Sync()
	f.Close()
}

// journal format: a single line holding the old-dir path (may be empty).
func writeJournal(path, old string) error {
	if err := os.WriteFile(path, []byte(old+"\n"), manifestFilePerm); err != nil {
		return err
	}
	return nil
}

func readJournal(path string) (old string, exists bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

func readJournalOld(path string) string {
	old, _ := readJournal(path)
	return old
}
