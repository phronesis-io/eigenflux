// Package skills implements CLI-owned distribution of EigenFlux skills.
//
// Skills are bundled alongside the CLI binary on R2 (cdn.eigenflux.ai/cli/<ver>/)
// and pulled into each host's real skill-load directory by `eigenflux skills sync`.
// This decouples skill updates from plugin releases: bumping the CLI patch ships
// new skills to every host on its next startup sync — zero plugin republish.
//
// The hot path is hardened against the failure modes that matter for a tool that
// overwrites a directory other agents read live: whole-directory atomic swap
// (rename×2 + journal + fsync parent), per-skill sha256 verification, a
// managed_by safety lock so reconcile never deletes skills it did not install,
// and first-install preservation of unrelated/third-party skill folders.
package skills

import "net/http"

const (
	// ManagedByValue marks a local manifest as written by this tool. reconcile
	// only ever removes skills recorded under a manifest carrying this marker —
	// user-placed or foreign skill folders are never touched.
	ManagedByValue = "eigenflux-cli"

	// ManifestFileName is the local manifest written into the skills dir.
	ManifestFileName = ".ef-manifest.json"
	// RemoteManifest is the manifest object name on R2.
	RemoteManifest = "manifest.json"
	// TarName is the skills archive object name on R2.
	TarName = "skills.tar.gz"
	// TarSHAName is the bare-hash sidecar object name on R2.
	TarSHAName = "skills.tar.gz.sha256"
	// StaleMarkerName is dropped by the GitHub bootstrap fallback so the next
	// online sync knows the local copy is provisional and must be replaced.
	StaleMarkerName = ".ef-stale"

	// Fixed staging / recovery slots. Safe to use fixed names because all access
	// is serialized by the exclusive lock — no pid randomization needed.
	newSuffix     = ".ef-new"      // real + newSuffix: staged next version
	oldSuffix     = ".ef-old"      // real + oldSuffix: previous version during swap
	journalSuffix = ".ef-swapping" // real + journalSuffix: swap-in-progress marker
	lockFileName  = ".ef-skills.lock"

	dirPerm          = 0o700 // mirror cache.go
	skillFilePerm    = 0o644 // skill files must be world-readable: agents read SKILL.md
	manifestFilePerm = 0o600 // local metadata, mirror cache.go credentials perms

	// staleLockTimeout: a lock file older than this is treated as crash debris
	// and force-claimed.
	staleLockSeconds = 600
)

// CDNDefault is the public CDN base; overridable via EIGENFLUX_CDN_URL.
const CDNDefault = "https://cdn.eigenflux.ai"

// ProdAllowlist is the fixed set of production skills shipped on R2.
// ef-localdev (a dev-only skill living in a separate repo) is intentionally
// excluded and is never distributed.
var ProdAllowlist = []string{"ef-broadcast", "ef-communication", "ef-profile", "ef-trading"}

// Manifest is the authoritative description of a skills bundle. The content
// `revision` (plus per-skill sha256) is authoritative; cli_version is
// informational and display_version is cosmetic — both may be empty.
type Manifest struct {
	// Revision is the content fingerprint of the bundle (hash over the sorted
	// per-skill sha256s). It is the freshness key — skills update when this
	// changes, INDEPENDENT of the CLI binary version. This is what makes a skill
	// edit a `release-skills` (upload to R2) rather than a CLI republish.
	Revision string `json:"revision"`
	// MinCLIVersion gates compatibility: a CLI older than this must not adopt
	// the bundle (the skills may reference newer CLI commands). Empty = no floor.
	MinCLIVersion string `json:"min_cli_version,omitempty"`
	// CLIVersion is the CLI that built the bundle — informational only, NOT used
	// for the update decision (that was the original design flaw).
	CLIVersion  string       `json:"cli_version,omitempty"`
	ManagedBy   string       `json:"managed_by"`
	GeneratedAt int64        `json:"generated_at,omitempty"`
	TarSHA256   string       `json:"tar_sha256,omitempty"`
	Skills      []SkillEntry `json:"skills"`
}

// SkillEntry is one skill's identity within a manifest.
type SkillEntry struct {
	Name           string `json:"name"`
	SHA256         string `json:"sha256"`
	DisplayVersion string `json:"display_version,omitempty"`
}

// names returns the set of skill names in the manifest.
func (m *Manifest) names() map[string]string {
	out := make(map[string]string, len(m.Skills))
	for _, s := range m.Skills {
		out[s.Name] = s.SHA256
	}
	return out
}

// SyncOptions configures a Sync run.
type SyncOptions struct {
	Into       string       // explicit target dir; highest precedence
	Host       string       // openclaw|claude-code|codex|terminal; "" => autodetect
	CLIVersion string       // current binary version, supplied by the cmd layer
	CDNBase    string       // default CDNDefault
	IfStale    bool         // background mode: keep local silently on fetch failure (revision match always short-circuits the download)
	Quiet      bool         // never return an error (exit 0); for startup hooks
	FromBundle bool         // offline-first-install: fall back to BundleDir
	BundleDir  string       // local skills dir for InstallFromBundle / fallback
	Allowlist  []string     // production skill names; defaults to ProdAllowlist
	HTTPClient *http.Client // injectable for tests
}

func (o SyncOptions) allowlist() []string {
	if len(o.Allowlist) > 0 {
		return o.Allowlist
	}
	return ProdAllowlist
}

func (o SyncOptions) cdnBase() string {
	if o.CDNBase != "" {
		return o.CDNBase
	}
	return CDNDefault
}

// SyncResult is the structured outcome of a Sync run.
type SyncResult struct {
	SkillsDir  string   `json:"skills_dir"`
	Source     string   `json:"source"` // cli/<ver> | cli/latest | local | bundle
	CLIVersion string   `json:"cli_version"`
	Removed    []string `json:"removed,omitempty"`
	// Preserved lists skills kept verbatim instead of updated — third-party
	// folders and skills the user hand-edited. Surfaced so a user stuck on a
	// local fork (which would otherwise never receive updates) can see why.
	Preserved []string `json:"preserved,omitempty"`
	Stale     bool     `json:"stale"`
	NoNetwork bool     `json:"no_network"`
	Atomic    bool     `json:"atomic"`
}
