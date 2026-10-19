package discoverylr

import (
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"sync"
	"sync/atomic"
	"time"
)

// Manager pins one immutable scorer for an entire candidate batch. Bad reloads
// retain the previous model; an absent initial model keeps rule-only serving.
type Manager struct {
	current  atomic.Pointer[model]
	contract string
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

// New serves the discovery_lr_v1 follow-up model.
func New(enabled bool, path string, interval time.Duration) *Manager {
	return newManager(Contract, enabled, path, interval)
}

// NewRating serves the discovery_rating_lr_v1 positive-rating model.
func NewRating(enabled bool, path string, interval time.Duration) *Manager {
	return newManager(RatingContract, enabled, path, interval)
}

// NewRatingV2 serves the discovery_rating_lr_v2 positive-rating model.
func NewRatingV2(enabled bool, path string, interval time.Duration) *Manager {
	return newManager(RatingV2Contract, enabled, path, interval)
}

func newManager(contract string, enabled bool, path string, interval time.Duration) *Manager {
	m := &Manager{contract: contract, stop: make(chan struct{}), done: make(chan struct{})}
	if !enabled {
		close(m.done)
		return m
	}
	if interval <= 0 {
		interval = time.Minute
	}
	m.reload(path)
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-ticker.C:
				m.reload(path)
			}
		}
	}()
	return m
}

// outcome keeps the original follow-up labels and prefixes rating outcomes by contract.
func (m *Manager) outcome(result string) string {
	switch m.contract {
	case RatingContract:
		return "rating_" + result
	case RatingV2Contract:
		return "rating_v2_" + result
	}
	return result
}

func (m *Manager) reload(path string) {
	next, err := load(path, m.contract)
	if err != nil {
		metrics.DiscoveryLRReload.WithLabelValues(m.outcome("error")).Inc()
		logger.Default().Warn("discovery LR load failed; retaining previous model", "contract", m.contract, "error", err)
		return
	}
	previous := m.current.Swap(next)
	if previous == nil || previous.Version != next.Version {
		metrics.DiscoveryLRReload.WithLabelValues(m.outcome("success")).Inc()
		logger.Default().Info("discovery LR model loaded", "contract", m.contract, "version", next.Version)
	}
}

func (m *Manager) Close() { m.once.Do(func() { close(m.stop) }); <-m.done }

// ScoreBatch is all-or-nothing so malformed evidence never mixes probabilities
// and rule scores inside the same request's broadcast ranking pool.
func (m *Manager) ScoreBatch(vectors [][]float64) ([]Result, bool) {
	current := m.current.Load()
	if current == nil {
		return nil, false
	}
	results := make([]Result, len(vectors))
	for i, vector := range vectors {
		p, ok := current.score(vector)
		if !ok {
			return nil, false
		}
		results[i] = Result{ModelVersion: current.Version, Contract: current.Contract, Probability: p}
	}
	return results, true
}
