package consumer

import (
	"context"
	"sync"
	"time"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
)

type streamDeliveries struct {
	mu     sync.Mutex
	active map[string]bool
}

func (s *streamDeliveries) snapshot() map[string]bool {
	result := make(map[string]bool)
	if s == nil {
		return result
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.active {
		result[id] = true
	}
	return result
}

type streamLease struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	id     string
	state  *streamDeliveries
}

func (l *streamLease) close() {
	l.cancel()
	<-l.done
	l.state.mu.Lock()
	delete(l.state.active, l.id)
	l.state.mu.Unlock()
}

func (c *StreamConsumer) startLease(ctx context.Context, id string, minIdle time.Duration) (*streamLease, error) {
	c.deliveries.mu.Lock()
	if c.deliveries.active[id] {
		c.deliveries.mu.Unlock()
		return nil, nil
	}
	c.deliveries.active[id] = true
	c.deliveries.mu.Unlock()
	leaseCtx, cancel := context.WithCancel(ctx)
	lease := &streamLease{ctx: leaseCtx, cancel: cancel, done: make(chan struct{}), id: id, state: c.deliveries}
	interval := minIdle / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	refresh := func() (bool, error) {
		callCtx, callCancel := context.WithTimeout(leaseCtx, interval)
		defer callCancel()
		return mq.RefreshOwnedPending(callCtx, c.Stream, c.Group, c.ConsumerName, id)
	}
	owned, err := refresh()
	if err != nil || !owned {
		close(lease.done)
		lease.close()
		return nil, err
	}
	go func() {
		defer close(lease.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				owned, err := refresh()
				if err != nil || !owned {
					if ctx.Err() == nil {
						logger.Default().Warn(c.Name+" pending ownership lost", "msgID", id, "err", err)
					}
					cancel()
					return
				}
			}
		}
	}()
	return lease, nil
}
