package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sync pulls the latest skills bundle from R2 into the host's real skill-load
// directory using a crash-safe whole-directory atomic swap. See package doc for
// the guarantees. It never deletes skills it did not install (managed_by lock)
// and preserves unrelated/user-modified skill folders.
func Sync(opts SyncOptions) (result *SyncResult, err error) {
	defer func() { err = permissionFailure(err, "install") }()
	real, parent, err := resolveDir(opts)
	if err != nil {
		return nil, err
	}

	// Repair interrupted local swaps before waiting on the network.
	local, err := readSyncSnapshot(real)
	if errors.Is(err, errRecoveryRequired) {
		lock, lockErr := beginSyncWrite(real, parent)
		if lockErr != nil {
			return nil, lockErr
		}
		local, err = readSyncSnapshot(real)
		lock.Release()
	}
	if err != nil {
		return nil, err
	}
	remote, dirURL, source, ferr := fetchManifest(opts)
	// The installation may change while the remote check is in flight.
	local, err = readSyncSnapshot(real)
	if err != nil {
		return nil, err
	}
	if ferr != nil {
		if local.intact {
			r := &SyncResult{SkillsDir: real, Source: "local", CLIVersion: local.manifest.CLIVersion, NoNetwork: true, Stale: local.stale}
			if opts.IfStale || opts.Quiet {
				return r, nil
			}
			return r, ferr
		}
		if local.manifest == nil && opts.FromBundle && opts.BundleDir != "" {
			lock, err := beginSyncWrite(real, parent)
			if err != nil {
				return nil, err
			}
			defer lock.Release()
			local, err = readSyncSnapshot(real)
			if err != nil {
				return nil, err
			}
			if local.intact {
				return &SyncResult{SkillsDir: real, Source: "local", CLIVersion: local.manifest.CLIVersion, NoNetwork: true, Stale: local.stale}, nil
			}
			if local.manifest == nil {
				return bundleApply(opts, real, parent, nil, true)
			}
		}
		return nil, ferr
	}
	if result, err := syncDecision(opts, real, local, remote); result != nil || err != nil {
		return result, err
	}

	lock, err := beginSyncWrite(real, parent)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	local, err = readSyncSnapshot(real)
	if err != nil {
		return nil, err
	}
	// A different writer may have installed a newer release during the check.
	if result, err := syncDecision(opts, real, local, remote); result != nil || err != nil {
		return result, err
	}
	if local.intact && !local.stale && local.manifest.Revision == remote.Revision {
		remote.ManagedBy = ManagedByValue
		if err := WriteManifestAtomic(real, remote); err != nil {
			return nil, fmt.Errorf("manifest metadata update failed: %w", err)
		}
		return &SyncResult{SkillsDir: real, Source: "local", CLIVersion: remote.CLIVersion, VerifiedManifest: true}, nil
	}

	// Keep the existing same-filesystem staging and durable extraction path.
	tarGz, err := fetchTarball(opts, dirURL, remote.Revision)
	if err != nil {
		return syncDownloadFailure(opts, real, err)
	}
	if err := verifyTarSHA(tarGz, remote.TarSHA256); err != nil {
		return syncDownloadFailure(opts, real, err)
	}
	newDir := real + newSuffix
	if err := os.RemoveAll(newDir); err != nil {
		return nil, err
	}
	if err := extractTarGz(tarGz, newDir, manifestSkillNames(remote)); err != nil {
		os.RemoveAll(newDir)
		return nil, err
	}
	return applyStaged(opts, real, parent, newDir, local.manifest, remote, source, false)
}

// syncDecision returns nil when a write is required. Both the optimistic read
// and the locked commit use the same rollback and compatibility rules.
func syncDecision(opts SyncOptions, real string, state *syncSnapshot, remote *Manifest) (*SyncResult, error) {
	local, intact := state.manifest, state.intact
	reason := ""
	if local != nil && local.Sequence > 0 {
		switch {
		case remote.Sequence < local.Sequence:
			reason = "signed manifest rollback rejected"
		case remote.Sequence == local.Sequence && remote.Revision != local.Revision:
			reason = "signed manifest sequence reused for different content"
		}
	}
	if reason == "" && !cliMeetsMin(opts.CLIVersion, remote.MinCLIVersion) {
		reason = fmt.Sprintf("skills need CLI >= %s (have %s) — upgrade the CLI", remote.MinCLIVersion, opts.CLIVersion)
	}
	if reason != "" {
		if intact {
			return keepLocal(real, local, reason), nil
		}
		return nil, fmt.Errorf("skills sync: %s; no intact local installation", reason)
	}
	if intact && !state.stale && local.Revision == remote.Revision && remote.Sequence == local.Sequence {
		return &SyncResult{SkillsDir: real, Source: "local", CLIVersion: local.CLIVersion, VerifiedManifest: true}, nil
	}
	return nil, nil
}

func syncDownloadFailure(opts SyncOptions, real string, cause error) (*SyncResult, error) {
	local, err := readSyncSnapshot(real)
	if err != nil {
		return nil, softFail(opts, err)
	}
	if local.intact {
		return keepLocal(real, local.manifest, cause.Error()), nil
	}
	return nil, softFail(opts, cause)
}

// beginSyncWrite creates directories only for an actual mutation and recovers
// interrupted transactions while holding the lock. Contention is retryable,
// never evidence that a complete local installation exists.
func beginSyncWrite(real, parent string) (*Lock, error) {
	if err := checkSyncReadAccess(real, parent); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(parent, dirPerm); err != nil {
		return nil, permissionFailure(err, "create_lock")
	}
	lock, locked, err := acquireLock(parent)
	if err != nil {
		return nil, permissionFailure(err, "create_lock")
	}
	if !locked {
		return nil, fmt.Errorf("skills sync: installation is being updated; retry sync")
	}
	if err := recoverInterrupted(real); err != nil {
		lock.Release()
		return nil, permissionFailure(err, "recover")
	}
	return lock, nil
}

func manifestSkillNames(m *Manifest) []string {
	names := make([]string, 0, len(m.Skills))
	for _, skill := range m.Skills {
		names = append(names, skill.Name)
	}
	return names
}

// applyStaged finishes a sync once newDir is populated: verify, preserve
// unmanaged/user-modified folders, then atomically swap newDir into place.
func applyStaged(opts SyncOptions, real, parent, newDir string, local, remote *Manifest, source string, stale bool) (*SyncResult, error) {
	if err := verifyManifest(newDir, remote); err != nil {
		os.RemoveAll(newDir)
		if errors.Is(err, os.ErrPermission) {
			return nil, permissionFailure(err, "read")
		}
		if local != nil {
			return keepLocal(real, local, "verify failed: "+err.Error()), nil
		}
		return nil, softFail(opts, err)
	}

	preserved, err := preserveUnmanaged(real, newDir, local, remote, opts.ForceManaged)
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
		if len(preserved) > 0 {
			res.VerifiedManifest = false
		}
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
		VerifiedManifest: !stale && remote.Sequence > 0 && remote.Signature != "",
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
func recoverInterrupted(real string) error {
	journal := real + journalSuffix
	data, err := os.ReadFile(journal)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	old := strings.TrimSpace(string(data))
	parent := filepath.Dir(real)
	// Preserve the journal and rollback slot if any recovery step fails.
	if !dirExists(real) && old != "" {
		if !dirExists(old) {
			return fmt.Errorf("skills sync: rollback directory is missing: %s", old)
		}
		if err := os.Rename(old, real); err != nil {
			return err
		}
		fsyncDir(parent)
	}
	if err := os.RemoveAll(real + newSuffix); err != nil {
		return err
	}
	if old != "" && old != real {
		if err := os.RemoveAll(old); err != nil {
			return err
		}
	}
	if err := os.Remove(journal); err != nil {
		return err
	}
	fsyncDir(parent)
	return nil
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

// verifyInstalledSkills re-hashes only manifest-owned directories. The active
// target may also contain third-party Skills, which are preserved intentionally
// but are never trusted as part of the official release.
func verifyInstalledSkills(dir string, manifest *Manifest) error {
	var invalid error
	for name, sha := range manifest.names() {
		sum, err := dirSHA256(filepath.Join(dir, name))
		if errors.Is(err, os.ErrPermission) {
			return err
		}
		// Continue after a mismatch so another official Skill's read denial
		// cannot be hidden by a user edit or a missing file.
		if err != nil {
			invalid = err
		} else if sum != sha {
			invalid = fmt.Errorf("installed skill %q was modified", name)
		}
	}
	return invalid
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
func preserveUnmanaged(real, newDir string, local, remote *Manifest, forceManaged bool) (skippedUpdate []string, err error) {
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
			if errors.Is(err, os.ErrPermission) {
				return nil, permissionFailure(err, "read")
			}
			if err == nil && cur != want && !forceManaged {
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

func resolveDir(opts SyncOptions) (real, parent string, err error) {
	dst, err := ResolveSkillsDir(opts.Into, opts.Host)
	if err != nil {
		return "", "", permissionFailure(err, "read")
	}
	real = dst
	if r, e := filepath.EvalSymlinks(dst); e == nil {
		real = r
	}
	parent = filepath.Dir(real)
	return real, parent, nil
}

func prepareDir(opts SyncOptions) (real, parent string, err error) {
	real, parent, err = resolveDir(opts)
	if err != nil {
		return "", "", err
	}
	if err := checkSyncReadAccess(real, parent); err != nil {
		return "", "", err
	}
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

// softFail preserves failures for callers even when quiet output is requested.
func softFail(_ SyncOptions, err error) error {
	return permissionFailure(err, "install")
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
