package skills

import (
	"fmt"
	"os"
	"path/filepath"
)

type UninstallResult struct {
	Target    string   `json:"target"`
	Removed   []string `json:"removed"`
	Preserved []string `json:"preserved"`
	Apply     bool     `json:"apply"`
}

// Uninstall uses the same directory lock and content hashes as synchronization.
// An edited, foreign, or symlinked root is retained even during explicit removal.
// keepManifest retains ownership evidence for a multi-target installation retry.
func Uninstall(target string, apply bool, keepManifest ...bool) (*UninstallResult, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("skills target must be a real directory")
	}
	if apply {
		lock, ok, err := acquireLock(filepath.Dir(abs))
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("skills synchronization is active")
		}
		defer lock.Release()
	}
	manifestInfo, err := os.Lstat(filepath.Join(abs, ManifestFileName))
	if err != nil {
		return nil, err
	}
	if !manifestInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("managed manifest must be a regular file")
	}
	m, err := ReadLocalManifest(abs)
	if err != nil {
		return nil, err
	}
	if m == nil || m.ManagedBy != ManagedByValue {
		return nil, fmt.Errorf("no EigenFlux managed manifest at target")
	}
	seen := map[string]bool{}
	for _, entry := range m.Skills {
		if !productionSkillName.MatchString(entry.Name) || seen[entry.Name] {
			return nil, fmt.Errorf("invalid or duplicate managed skill name")
		}
		seen[entry.Name] = true
	}
	r := &UninstallResult{Target: abs, Apply: apply, Removed: []string{}, Preserved: []string{}}
	for _, entry := range m.Skills {
		if !productionSkillName.MatchString(entry.Name) {
			return nil, fmt.Errorf("invalid managed skill name")
		}
		path := filepath.Join(abs, entry.Name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			r.Preserved = append(r.Preserved, entry.Name)
			continue
		}
		digest, err := dirSHA256(path)
		if err != nil || digest != entry.SHA256 {
			r.Preserved = append(r.Preserved, entry.Name)
			continue
		}
		if apply {
			if err := os.RemoveAll(path); err != nil {
				return r, err
			}
		}
		r.Removed = append(r.Removed, entry.Name)
	}
	if apply && len(r.Preserved) == 0 && !(len(keepManifest) > 0 && keepManifest[0]) {
		if err := os.Remove(filepath.Join(abs, ManifestFileName)); err != nil && !os.IsNotExist(err) {
			return r, err
		}
	}
	return r, nil
}
