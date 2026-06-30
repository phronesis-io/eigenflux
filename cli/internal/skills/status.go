package skills

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// LocalSkill is the display status of one installed skill.
type LocalSkill struct {
	Name           string `json:"name"`
	DisplayVersion string `json:"display_version,omitempty"`
	SHA256         string `json:"sha256"`
	// SHAMatch reports whether the on-disk content still matches the recorded
	// hash. False means the user (or something else) edited the skill, or the
	// install is corrupt — surfaced so `version` alone can't mislead.
	SHAMatch bool `json:"sha_match"`
}

// ListLocal resolves the host skills dir and reports the locally installed,
// CLI-managed skills with a live sha_match recomputation.
func ListLocal(into, host string) (dir string, skills []LocalSkill, managed bool, err error) {
	dir, err = ResolveSkillsDir(into, host)
	if err != nil {
		return "", nil, false, err
	}
	m, err := ReadLocalManifest(dir)
	if err != nil || m == nil {
		return dir, nil, false, err
	}
	managed = m.ManagedBy == ManagedByValue
	for _, s := range m.Skills {
		match := false
		if sum, e := dirSHA256(filepath.Join(dir, s.Name)); e == nil {
			match = sum == s.SHA256
		}
		skills = append(skills, LocalSkill{
			Name:           s.Name,
			DisplayVersion: s.DisplayVersion,
			SHA256:         s.SHA256,
			SHAMatch:       match,
		})
	}
	return dir, skills, managed, nil
}

// LocalManifestVersion returns the cli_version recorded in the host skills dir,
// or "" when none is installed.
func LocalManifestVersion(into, host string) string {
	dir, err := ResolveSkillsDir(into, host)
	if err != nil {
		return ""
	}
	m, err := ReadLocalManifest(dir)
	if err != nil || m == nil {
		return ""
	}
	return m.CLIVersion
}

// FetchLatestVersion reads cli/latest/version.txt from the CDN. Best-effort:
// returns "" on any failure so doctor degrades to "unknown".
func FetchLatestVersion(cdnBase string, hc *http.Client) string {
	if cdnBase == "" {
		cdnBase = CDNDefault
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	url := strings.TrimRight(cdnBase, "/") + "/cli/latest/version.txt"
	resp, err := hc.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
