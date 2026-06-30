package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// errNoNetwork signals the fetch chain exhausted both cli/<ver> and cli/latest.
var errNoNetwork = errors.New("skills sync: remote unavailable")

const fetchTimeout = 30 * time.Second

// fetchWithFallback pulls the manifest + tarball, trying the version-pinned path
// first and falling back to latest/. It rejects empty/garbage manifests up front
// so a truncated CDN response can never drive a swap that wipes the user's skills.
func fetchWithFallback(opts SyncOptions) (m *Manifest, tarGz []byte, source string, err error) {
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: fetchTimeout}
	}
	base := strings.TrimRight(opts.cdnBase(), "/")

	candidates := []struct{ path, source string }{}
	if opts.CLIVersion != "" {
		candidates = append(candidates, struct{ path, source string }{
			path: fmt.Sprintf("%s/cli/%s", base, opts.CLIVersion), source: "cli/" + opts.CLIVersion})
	}
	candidates = append(candidates, struct{ path, source string }{
		path: base + "/cli/latest", source: "cli/latest"})

	var lastErr error
	for _, c := range candidates {
		man, tar, e := fetchOne(hc, c.path, opts.CLIVersion)
		if e != nil {
			lastErr = e
			continue
		}
		return man, tar, c.source, nil
	}
	if lastErr == nil {
		lastErr = errNoNetwork
	}
	return nil, nil, "", fmt.Errorf("%w: %v", errNoNetwork, lastErr)
}

func fetchOne(hc *http.Client, dirURL, cliVersion string) (*Manifest, []byte, error) {
	manBytes, err := httpGet(hc, dirURL+"/"+RemoteManifest, cliVersion)
	if err != nil {
		return nil, nil, err
	}
	var m Manifest
	if err := json.Unmarshal(manBytes, &m); err != nil {
		return nil, nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := validateRemoteManifest(&m); err != nil {
		return nil, nil, err
	}
	tarGz, err := httpGet(hc, dirURL+"/"+TarName, cliVersion)
	if err != nil {
		return nil, nil, err
	}
	if len(tarGz) == 0 {
		return nil, nil, fmt.Errorf("empty tarball at %s", dirURL)
	}
	return &m, tarGz, nil
}

// validateRemoteManifest refuses a manifest that would, if applied, leave the
// user with zero or malformed skills. This is the guard against CDN edge
// truncation silently clearing skills.
func validateRemoteManifest(m *Manifest) error {
	if strings.TrimSpace(m.CLIVersion) == "" {
		return fmt.Errorf("manifest missing cli_version")
	}
	if len(m.Skills) == 0 {
		return fmt.Errorf("manifest has no skills")
	}
	for _, s := range m.Skills {
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.SHA256) == "" {
			return fmt.Errorf("manifest entry missing name/sha256")
		}
	}
	return nil
}

func httpGet(hc *http.Client, url, cliVersion string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if cliVersion != "" {
		req.Header.Set("X-Client-CLI-Version", cliVersion)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	// Bound the read to a sane size (skills bundle is small).
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// verifyTarSHA checks the whole-archive checksum against the manifest's
// tar_sha256 (when present). Cheap defense against transport corruption /
// whole-archive substitution; per-skill verification still runs after extract.
func verifyTarSHA(tarGz []byte, want string) error {
	want = strings.TrimSpace(want)
	if want == "" {
		return nil // older manifest without tar_sha256: rely on per-skill verify
	}
	sum := sha256.Sum256(tarGz)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("tarball sha256 mismatch: got %s want %s", got, want)
	}
	return nil
}
