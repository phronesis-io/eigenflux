package skills

import (
	"fmt"
	"os"
	"path/filepath"
)

// InstallFromBundle installs skills from a local directory (the repo's own
// skills/ tree) instead of R2. Used by install-local.sh for development. It runs
// the same verify → preserve → atomic-swap path as Sync, but builds the manifest
// locally and never checks a remote checksum or marks the copy stale.
func InstallFromBundle(opts SyncOptions) (*SyncResult, error) {
	if opts.BundleDir == "" {
		return nil, fmt.Errorf("skills install: --from-bundle <dir> required")
	}
	real, parent, err := prepareDir(opts)
	if err != nil {
		return nil, softFail(opts, err)
	}
	lock, locked, err := acquireLock(parent)
	if err != nil {
		return nil, softFail(opts, err)
	}
	if !locked {
		return &SyncResult{SkillsDir: real, Source: "local"}, nil
	}
	defer lock.Release()

	recoverInterrupted(real)
	local, _ := ReadLocalManifest(real)
	return bundleApply(opts, real, parent, local, false)
}

// bundleApply stages the allowlisted skills from opts.BundleDir into newDir,
// generates a manifest from the staged copy, and swaps it in. Caller holds the
// lock. stale=true marks the result provisional (offline-first-install fallback
// from Sync), so the next online sync replaces it.
func bundleApply(opts SyncOptions, real, parent string, local *Manifest, stale bool) (*SyncResult, error) {
	newDir := real + newSuffix
	os.RemoveAll(newDir)
	if err := os.MkdirAll(newDir, dirPerm); err != nil {
		return nil, softFail(opts, err)
	}
	for _, name := range opts.allowlist() {
		src := filepath.Join(opts.BundleDir, name)
		if !fileExists(filepath.Join(src, "SKILL.md")) {
			os.RemoveAll(newDir)
			return nil, softFail(opts, fmt.Errorf("bundle skill %q missing SKILL.md in %s", name, opts.BundleDir))
		}
		if err := copyDir(src, filepath.Join(newDir, name)); err != nil {
			os.RemoveAll(newDir)
			return nil, softFail(opts, err)
		}
	}
	m, err := GenerateManifest(newDir, opts.CLIVersion, opts.allowlist(), 0)
	if err != nil {
		os.RemoveAll(newDir)
		return nil, softFail(opts, err)
	}
	source := "bundle"
	return applyStaged(opts, real, parent, newDir, local, m, source, stale)
}
