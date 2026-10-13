package featureindex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	featureconfig "eigenflux_server/configs/featureindex"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"gopkg.in/yaml.v3"
)

type snapshot struct {
	definitions []Definition
	digest      [32]byte
}

// Registry publishes complete immutable snapshots to concurrent readers/writers.
// Reload calls must be serialized by the owner (Start does this).
type Registry struct{ current atomic.Pointer[snapshot] }

var defaultRegistry = newDefaultRegistry()

func newDefaultRegistry() *Registry {
	r, err := NewRegistry(featureconfig.Defaults)
	if err != nil {
		panic(fmt.Sprintf("invalid bundled feature configuration: %v", err))
	}
	return r
}

// NewRegistry validates a complete set of YAML view definitions before use.
func NewRegistry(source fs.FS) (*Registry, error) {
	next, err := readSnapshot(source)
	if err != nil {
		return nil, err
	}
	r := &Registry{}
	r.current.Store(next)
	for _, d := range next.definitions {
		for _, outcome := range []string{"hit", "miss", "expired", "error", "request_hit"} {
			metrics.FeatureReadItems.WithLabelValues(d.Entity, d.Name, outcome).Add(0)
		}
	}
	return r, nil
}

func (r *Registry) Definitions() []Definition {
	out := append([]Definition(nil), r.current.Load().definitions...)
	for i := range out {
		out[i].Fields = append([]string(nil), out[i].Fields...)
		out[i].plans = nil
		if out[i].Load != nil {
			cfg := *out[i].Load
			out[i].Load = &cfg
		}
	}
	return out
}

var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func readSnapshot(source fs.FS) (*snapshot, error) {
	files, err := fs.Glob(source, "*.yaml")
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("feature configuration contains no YAML views")
	}
	next := &snapshot{}
	seen := map[string]bool{}
	hash := sha256.New()
	for _, file := range files {
		raw, err := fs.ReadFile(source, file)
		if err != nil {
			return nil, err
		}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		var d Definition
		if err := decoder.Decode(&d); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("%s: expected exactly one YAML document", file)
		}
		if d.Name != d.Entity+"."+d.Component || !fieldName.MatchString(d.Entity) || !fieldName.MatchString(d.Component) || d.TTL < 0 || d.RetentionTTL < 0 || (d.RetentionTTL > 0 && d.RetentionTTL < time.Millisecond) || (d.RetentionTTL > 0 && d.RetentionTTL < d.TTL) || seen[d.Name] {
			return nil, fmt.Errorf("%s: invalid or duplicate feature view", file)
		}
		seen[d.Name] = true
		fields := map[string]bool{}
		for _, field := range d.Fields {
			if !fieldName.MatchString(field) || fields[field] || field == "embedding" || field == "vector" {
				return nil, fmt.Errorf("%s: invalid, duplicate or prohibited field %q", file, field)
			}
			fields[field] = true
		}
		if d.IDField == d.VersionField || !fields[d.IDField] || !fields[d.VersionField] {
			return nil, fmt.Errorf("%s: identity/version fields must be present and distinct", file)
		}
		if d.Load != nil && (d.Load.Interval < 100*time.Millisecond || d.Load.Timeout <= 0 || d.Load.BatchSize < 1 || d.Load.BatchSize > 1000 || d.Load.CyclePause < d.Load.Interval) {
			return nil, fmt.Errorf("%s: invalid load schedule", file)
		}
		d.plans = &sync.Map{}
		next.definitions = append(next.definitions, d)
		fmt.Fprintf(hash, "%d:%s%d:", len(file), file, len(raw))
		hash.Write(raw)
	}
	copy(next.digest[:], hash.Sum(nil))
	return next, nil
}

// Reload retains the last good snapshot on any failure. Existing view identities
// and ID/version contracts cannot change while their source adapters are running.
func (r *Registry) Reload(source fs.FS) (bool, error) {
	next, err := readSnapshot(source)
	if err != nil {
		return false, err
	}
	prior := r.current.Load()
	if next.digest == prior.digest {
		return false, nil
	}
	if len(next.definitions) != len(prior.definitions) {
		return false, fmt.Errorf("feature view set cannot change at runtime")
	}
	byName := map[string]Definition{}
	for _, d := range next.definitions {
		byName[d.Name] = d
	}
	for _, old := range prior.definitions {
		d, ok := byName[old.Name]
		if !ok || d.Entity != old.Entity || d.Component != old.Component || d.IDField != old.IDField || d.VersionField != old.VersionField {
			return false, fmt.Errorf("feature identity contract cannot change: %s", old.Name)
		}
	}
	r.current.Store(next)
	logger.Default().Info("feature configuration loaded", "digest", fmt.Sprintf("%x", next.digest))
	return true, nil
}

func (r *Registry) reloadDir(dir string) error {
	// Resolve a versioned directory symlink once per snapshot rather than reading
	// files across different directory revisions.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	_, err = r.Reload(os.DirFS(resolved))
	return err
}

// Start loads external YAML synchronously; invalid/missing startup config fails.
// Later failures are logged and retain the last good snapshot. The returned
// function cancels and joins the polling goroutine.
func (r *Registry) Start(ctx context.Context, dir string, interval time.Duration) (func(), error) {
	if interval <= 0 {
		return nil, fmt.Errorf("feature reload interval must be positive")
	}
	if err := r.reloadDir(dir); err != nil {
		return nil, err
	}
	work, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-work.Done():
				return
			case <-ticker.C:
				if err := r.reloadDir(dir); err != nil {
					logger.Default().Error("feature configuration reload rejected", "directory", dir, "err", err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

// StartConfig configures the shared registry used by all domain forward adapters.
func StartConfig(ctx context.Context, dir, reloadInterval string) (func(), error) {
	interval, err := time.ParseDuration(reloadInterval)
	if err != nil {
		return nil, err
	}
	return defaultRegistry.Start(ctx, dir, interval)
}
