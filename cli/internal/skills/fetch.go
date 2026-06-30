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

func httpClient(opts SyncOptions) *http.Client {
	if opts.HTTPClient != nil {
		return opts.HTTPClient
	}
	return &http.Client{Timeout: fetchTimeout}
}

// fetchManifest pulls ONLY the small manifest.json (the cheap freshness check),
// trying the CLI-independent skills/latest path first and falling back to
// cli/latest for back-compat. Skills are published under their own path so a
// skill edit is a `release-skills` (upload), not a CLI republish — the manifest
// `revision` (not the CLI version) decides whether the tarball needs pulling.
// Empty/garbage manifests are rejected so a truncated CDN response can never
// drive a swap that wipes the user's skills.
func fetchManifest(opts SyncOptions) (m *Manifest, dirURL, source string, err error) {
	hc := httpClient(opts)
	base := strings.TrimRight(opts.cdnBase(), "/")
	candidates := []struct{ path, source string }{
		{base + "/skills/latest", "skills/latest"},
		{base + "/cli/latest", "cli/latest"}, // back-compat with the initial layout
	}
	var lastErr error
	for _, c := range candidates {
		manBytes, e := httpGet(hc, c.path+"/"+RemoteManifest, opts.CLIVersion)
		if e != nil {
			lastErr = e
			continue
		}
		var man Manifest
		if e := json.Unmarshal(manBytes, &man); e != nil {
			lastErr = fmt.Errorf("parse manifest: %w", e)
			continue
		}
		if e := validateRemoteManifest(&man); e != nil {
			lastErr = e
			continue
		}
		return &man, c.path, c.source, nil
	}
	if lastErr == nil {
		lastErr = errNoNetwork
	}
	return nil, "", "", fmt.Errorf("%w: %v", errNoNetwork, lastErr)
}

// fetchTarball downloads the bundle from the same directory the manifest came
// from. Only called when the revision changed, so the big download is skipped
// when skills are already current.
func fetchTarball(opts SyncOptions, dirURL string) ([]byte, error) {
	tarGz, err := httpGet(httpClient(opts), dirURL+"/"+TarName, opts.CLIVersion)
	if err != nil {
		return nil, err
	}
	if len(tarGz) == 0 {
		return nil, fmt.Errorf("empty tarball at %s", dirURL)
	}
	return tarGz, nil
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
