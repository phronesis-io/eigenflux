package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var errSkillsPermissionRequired = errors.New("SKILLS_PERMISSION_REQUIRED")

// permissionFailure retains the original filesystem error and gives the Agent
// an explicit next step through the existing command error channel.
func permissionFailure(err error, operation string) error {
	if !errors.Is(err, os.ErrPermission) || errors.Is(err, errSkillsPermissionRequired) {
		return err
	}
	return fmt.Errorf("%w: operation=%s; %w. Agent: explain the denied path and operation to the user; request the required filesystem access through the host permission flow; retry the exact original command only after approval. If denied, report that Skills synchronization is incomplete", errSkillsPermissionRequired, operation, err)
}

// checkSyncReadAccess reuses official content verification, without scanning
// unrelated Skills. Absent paths are valid for a first installation.
func checkSyncReadAccess(real, parent string) (err error) {
	defer func() { err = permissionFailure(err, "read") }()
	if _, err := os.ReadDir(parent); err != nil && !os.IsNotExist(err) {
		return err
	}
	paths := []string{real}
	data, err := os.ReadFile(real + journalSuffix)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err := checkCleanupReadAccess(real + newSuffix); err != nil {
			return err
		}
	}
	if old := strings.TrimSpace(string(data)); old != "" {
		paths = append(paths, old)
	}
	for _, root := range paths {
		if _, err := os.ReadDir(root); err != nil && !os.IsNotExist(err) {
			return err
		}
		manifest, err := ReadLocalManifest(root)
		if err != nil {
			return err
		}
		if manifest != nil {
			if err := verifyInstalledSkills(root, manifest); errors.Is(err, os.ErrPermission) {
				return err
			}
		}
	}
	return nil
}

// checkReplacementReadAccess runs only before a content replacement. Official
// files normally use the local manifest; the remote list covers a lost manifest.
// Only third-party directories that preserveUnmanaged will copy need extra reads.
func checkReplacementReadAccess(real string, local, remote *Manifest) (err error) {
	defer func() { err = permissionFailure(err, "read") }()
	if err := checkCleanupReadAccess(real + newSuffix); err != nil {
		return err
	}
	entries, err := os.ReadDir(real)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	inRemote := remote.names()
	recorded := map[string]string{}
	if local != nil && local.ManagedBy == ManagedByValue {
		recorded = local.names()
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		_, official := inRemote[entry.Name()]
		_, managed := recorded[entry.Name()]
		if (local == nil && official) || (!official && !managed) {
			if _, err := dirSHA256(filepath.Join(real, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// Cleanup traverses directories but does not read the contents of deleted files.
// WalkDir does not follow symlinks, matching RemoveAll.
func checkCleanupReadAccess(root string) error {
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if path == root && os.IsNotExist(err) {
			return nil
		}
		return err
	})
}
