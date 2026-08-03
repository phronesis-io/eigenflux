package lrranker

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
)

// Config controls model delivery and hot reload. The model itself is delivered
// out-of-band (an external script syncs the OSS bundle to a local directory and
// flips the `current` symlink); the Manager only ever reads local files.
type Config struct {
	Enabled        bool
	ModelPath      string        // path to current/model.json (usually via a symlink)
	ReloadInterval time.Duration // how often to check for a newer bundle
}

// Manager owns the currently live scorer and hot-swaps it when a newer bundle
// appears. It never fails the process on a missing or broken model: Score simply
// reports unavailable and the caller falls back to the baseline formula ranker.
type Manager struct {
	cfg Config
	cur atomic.Pointer[scorer]

	stop chan struct{}
	done chan struct{}

	// change-detection state, only touched by the reload goroutine.
	mu          sync.Mutex
	lastTarget  string
	lastModNano int64
}

// NewManager builds a Manager and, when enabled, performs an initial load and
// starts the background reload loop. A load failure at startup is logged and
// leaves the Manager in fallback mode rather than aborting sort startup.
func NewManager(cfg Config) *Manager {
	m := &Manager{cfg: cfg, stop: make(chan struct{}), done: make(chan struct{})}
	if !cfg.Enabled {
		close(m.done)
		logger.Default().Info("lrranker disabled; using baseline formula ranker")
		return m
	}
	if cfg.ReloadInterval <= 0 {
		cfg.ReloadInterval = 60 * time.Second
		m.cfg = cfg
	}
	m.tryReload("startup")
	go m.loop()
	return m
}

func (m *Manager) loop() {
	defer close(m.done)
	t := time.NewTicker(m.cfg.ReloadInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.tryReload("interval")
			m.refreshAgeMetric()
		}
	}
}

// tryReload loads and swaps the model only when the underlying bundle has
// changed since the last attempt. Change is recorded even on failure so a
// persistently broken bundle is retried only when it actually changes again,
// while the previous good scorer keeps serving.
func (m *Manager) tryReload(trigger string) {
	changed, target, modNano := m.detectChange()
	if !changed {
		return
	}
	m.mu.Lock()
	m.lastTarget = target
	m.lastModNano = modNano
	m.mu.Unlock()

	sc, err := LoadModel(m.cfg.ModelPath)
	if err != nil {
		metrics.LRRankerReloadTotal.WithLabelValues("error").Inc()
		logger.Default().Warn("lrranker: model load failed; keeping previous model", "err", err, "path", m.cfg.ModelPath, "trigger", trigger)
		return
	}
	note, err := sc.selfTest(1e-9)
	if err != nil {
		metrics.LRRankerReloadTotal.WithLabelValues("selftest_failed").Inc()
		logger.Default().Warn("lrranker: model self-test failed; keeping previous model", "err", err, "version", sc.version, "trigger", trigger)
		return
	}
	if note != "" {
		logger.Default().Info("lrranker: model self-test note", "note", note, "version", sc.version)
	}

	prev := m.cur.Swap(sc)
	metrics.LRRankerReloadTotal.WithLabelValues("success").Inc()
	metrics.SetLRRankerModelInfo(sc.version)
	m.refreshAgeMetric()
	prevVersion := ""
	if prev != nil {
		prevVersion = prev.version
	}
	logger.Default().Info("lrranker: model loaded", "version", sc.version, "previous", prevVersion, "trigger", trigger)
}

func (m *Manager) detectChange() (bool, string, int64) {
	target := m.cfg.ModelPath
	if resolved, err := filepath.EvalSymlinks(m.cfg.ModelPath); err == nil {
		target = resolved
	}
	var modNano int64
	if fi, err := os.Stat(m.cfg.ModelPath); err == nil {
		modNano = fi.ModTime().UnixNano()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if target == m.lastTarget && modNano == m.lastModNano {
		return false, target, modNano
	}
	return true, target, modNano
}

func (m *Manager) refreshAgeMetric() {
	sc := m.cur.Load()
	if sc == nil || sc.createdAtMS == 0 {
		return
	}
	age := time.Since(time.UnixMilli(sc.createdAtMS)).Seconds()
	metrics.LRRankerModelAge.Set(age)
}

// Score returns the LR result for one candidate. The second return value is
// false when no valid model is loaded (disabled, never loaded, or load failed),
// signaling the caller to fall back to the baseline ranker.
func (m *Manager) Score(in Input) (Result, bool) {
	sc := m.cur.Load()
	if sc == nil {
		return Result{}, false
	}
	return sc.Score(in), true
}

// Available reports whether a valid model is currently loaded.
func (m *Manager) Available() bool { return m != nil && m.cur.Load() != nil }

// Enabled reports whether the LR ranker is configured on (independent of
// whether a model has successfully loaded).
func (m *Manager) Enabled() bool { return m != nil && m.cfg.Enabled }

// ModelVersion returns the live model version, or "" when none is loaded.
func (m *Manager) ModelVersion() string {
	if m == nil {
		return ""
	}
	if sc := m.cur.Load(); sc != nil {
		return sc.version
	}
	return ""
}

// Close stops the reload loop and waits for it to exit.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	select {
	case <-m.done:
		return // never started (disabled) or already closed
	default:
	}
	close(m.stop)
	<-m.done
}
