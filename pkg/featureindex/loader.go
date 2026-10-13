package featureindex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"github.com/redis/go-redis/v9"
)

// Loader schedules one implementation of the common materialization contract.
type Loader struct {
	Index             Materializer
	Interval, Timeout time.Duration
	BatchSize         int
	CyclePause        time.Duration
	DynamicConfig     bool
}

type Loaders struct {
	Redis *redis.Client
	jobs  []Loader
}

func (m *Loaders) Register(l Loader) error {
	if l.Index == nil {
		return fmt.Errorf("feature loader index is required")
	}
	found := false
	for _, d := range Definitions() {
		if d.Name == l.Index.View() {
			found = true
		}
	}
	if !found || l.Index.Generation() == "" || l.Interval <= 0 || l.Timeout <= 0 || l.BatchSize < 1 || l.BatchSize > 1000 {
		return fmt.Errorf("invalid feature loader registration: %s", l.Index.View())
	}
	for _, prior := range m.jobs {
		if prior.Index.View() == l.Index.View() && prior.Index.Generation() == l.Index.Generation() {
			return fmt.Errorf("duplicate feature loader: %s", l.Index.View())
		}
	}
	m.jobs = append(m.jobs, l)
	metrics.FeatureLoaderLastSuccess.WithLabelValues(l.Index.View()).Set(0)
	metrics.FeatureLoaderLastCycle.WithLabelValues(l.Index.View()).Set(0)
	return nil
}

var finishLoad = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
if ARGV[2] ~= '' then
 redis.call('SET', KEYS[2], ARGV[2])
 if ARGV[3] and ARGV[3] ~= '' then redis.call('SET', KEYS[3], ARGV[3]) end
end
redis.call('DEL', KEYS[1])
return 1
`)

// Step executes one bounded page. The lease also fences checkpoint advancement;
// source-version guards independently protect feature writes after lease loss.
func (m *Loaders) Step(ctx context.Context, l Loader) error { _, err := m.step(ctx, l); return err }
func (m *Loaders) step(ctx context.Context, l Loader) (complete bool, resultErr error) {
	if m.Redis == nil || l.Index == nil {
		return false, fmt.Errorf("feature loader Redis and index are required")
	}
	outcome := "success"
	defer func() {
		if resultErr != nil {
			outcome = "error"
		}
		metrics.FeatureLoaderSteps.WithLabelValues(l.Index.View(), outcome).Inc()
	}()
	prefix := "discovery:feature:loader:" + l.Index.View() + ":" + l.Index.Generation()
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return false, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	acquired, err := m.Redis.SetNX(ctx, prefix+":lock", token, l.Timeout+5*time.Second).Result()
	if err != nil || !acquired {
		outcome = "lease_busy"
		return false, err
	}
	next := ""
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		// On failure only release; never advance an incomplete page.
		if next == "" {
			_ = finishLoad.Run(cleanup, m.Redis, []string{prefix + ":lock", prefix + ":cursor"}, token, "").Err()
		}
	}()
	work, cancel := context.WithTimeout(ctx, l.Timeout)
	defer cancel()
	if l.DynamicConfig {
		due, err := m.Redis.Get(work, prefix+":next_due").Int64()
		if err != nil && err != redis.Nil {
			return false, err
		}
		if due > time.Now().UnixMilli() {
			outcome = "scheduled_wait"
			return false, nil
		}
	}
	raw, err := m.Redis.Get(work, prefix+":cursor").Result()
	if err != nil && err != redis.Nil {
		return false, err
	}
	var cursor int64
	if raw != "" {
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 0 {
			return false, fmt.Errorf("invalid feature loader cursor")
		}
	}
	value, err := l.Index.LoadPage(work, cursor, l.BatchSize)
	if err != nil {
		return false, err
	}
	if value < 0 || value != 0 && value <= cursor {
		return false, fmt.Errorf("feature loader cursor did not advance")
	}
	nextValue := strconv.FormatInt(value, 10)
	due := ""
	if l.DynamicConfig {
		delay := l.Interval
		if value == 0 && l.CyclePause > delay {
			delay = l.CyclePause
		}
		due = strconv.FormatInt(time.Now().Add(delay).UnixMilli(), 10)
	}
	n, err := finishLoad.Run(work, m.Redis, []string{prefix + ":lock", prefix + ":cursor", prefix + ":next_due"}, token, nextValue, due).Int()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, fmt.Errorf("feature loader lease lost")
	}
	next = nextValue
	metrics.FeatureLoaderLastSuccess.WithLabelValues(l.Index.View()).SetToCurrentTime()
	if value == 0 {
		metrics.FeatureLoaderLastCycle.WithLabelValues(l.Index.View()).SetToCurrentTime()
	}
	return value == 0, nil
}

// Run starts a bounded loop per registered view and waits for clean shutdown.
// Registration is startup-only; Run must not race with Register.
func (m *Loaders) Run(ctx context.Context) {
	done := make(chan struct{}, len(m.jobs)+1)
	for _, job := range m.jobs {
		go func(l Loader) {
			defer func() { done <- struct{}{} }()
			failures := 0
			for ctx.Err() == nil {
				if l.DynamicConfig {
					for _, d := range Definitions() {
						if d.Name == l.Index.View() && d.Load != nil {
							l.Interval, l.Timeout, l.BatchSize, l.CyclePause = d.Load.Interval, d.Load.Timeout, d.Load.BatchSize, d.Load.CyclePause
						}
					}
				}
				complete, err := m.step(ctx, l)
				delay := l.Interval
				if err != nil {
					failures++
					delay = time.Duration(1<<min(failures, 5)) * time.Second
					logger.Default().Warn("feature materialization failed", "view", l.Index.View(), "err", err)
				} else {
					failures = 0
					if complete && l.CyclePause > delay {
						delay = l.CyclePause
					}
				}
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}(job)
	}
	go func() { defer func() { done <- struct{}{} }(); m.runAudit(ctx) }()
	for i := 0; i < len(m.jobs)+1; i++ {
		<-done
	}
}
