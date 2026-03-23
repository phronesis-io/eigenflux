package idgen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// IDGenerator is the interface for generating unique IDs.
type IDGenerator interface {
	NextID() (int64, error)
}

const (
	workerIDBits  = 10
	sequenceBits  = 12
	maxWorkerID   = -1 ^ (-1 << workerIDBits)
	sequenceMask  = -1 ^ (-1 << sequenceBits)
	workerIDShift = sequenceBits
	timeShift     = sequenceBits + workerIDBits
)

// Snowflake is a 64-bit ID generator:
// 1 sign bit (unused) + 41 timestamp bits + 10 worker bits + 12 sequence bits.
type Snowflake struct {
	mu sync.Mutex

	epochMS   int64
	workerID  int64
	lastMS    int64
	sequence  int64
	nowMillis func() int64
}

func NewSnowflake(workerID int64, epochMS int64) (*Snowflake, error) {
	if workerID < 0 || workerID > maxWorkerID {
		return nil, fmt.Errorf("worker_id out of range: %d", workerID)
	}
	if epochMS <= 0 {
		return nil, errors.New("invalid epoch")
	}

	return &Snowflake{
		epochMS:   epochMS,
		workerID:  workerID,
		nowMillis: func() int64 { return time.Now().UnixMilli() },
	}, nil
}

func (s *Snowflake) WorkerID() int64 {
	return s.workerID
}

func (s *Snowflake) NextID() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowMillis()
	if now < s.lastMS {
		now = s.waitUntil(s.lastMS)
	}

	if now == s.lastMS {
		s.sequence = (s.sequence + 1) & sequenceMask
		if s.sequence == 0 {
			now = s.waitUntil(s.lastMS + 1)
		}
	} else {
		s.sequence = 0
	}

	if now < s.epochMS {
		return 0, errors.New("current time is before epoch")
	}

	s.lastMS = now
	id := ((now - s.epochMS) << timeShift) | (s.workerID << workerIDShift) | s.sequence
	if id <= 0 {
		return 0, errors.New("generated invalid id")
	}
	return id, nil
}

func (s *Snowflake) waitUntil(targetMS int64) int64 {
	now := s.nowMillis()
	for now < targetMS {
		time.Sleep(time.Millisecond)
		now = s.nowMillis()
	}
	return now
}

type ManagedGenerator struct {
	sf       *Snowflake
	client   *clientv3.Client
	leaseID  clientv3.LeaseID
	workerID int64
	key      string
	value    string

	alive  atomic.Bool
	cancel context.CancelFunc
	done   chan struct{}
}

type ManagedGeneratorConfig struct {
	Endpoints      []string
	WorkerPrefix   string
	ServiceName    string
	InstanceID     string
	LeaseTTLSecond int
	EpochMS        int64
}

func NewManagedGenerator(ctx context.Context, cfg ManagedGeneratorConfig) (*ManagedGenerator, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, errors.New("empty etcd endpoints")
	}
	if strings.TrimSpace(cfg.WorkerPrefix) == "" {
		cfg.WorkerPrefix = "/eigenflux/idgen/workers"
	}
	if strings.TrimSpace(cfg.ServiceName) == "" {
		return nil, errors.New("empty service name")
	}
	if cfg.LeaseTTLSecond <= 0 {
		cfg.LeaseTTLSecond = 30
	}
	if cfg.EpochMS <= 0 {
		return nil, errors.New("invalid epoch")
	}
	if strings.TrimSpace(cfg.InstanceID) == "" {
		hostname, _ := os.Hostname()
		cfg.InstanceID = fmt.Sprintf("%s-%d-%d", hostname, os.Getpid(), time.Now().UnixNano())
	}

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create etcd client: %w", err)
	}

	leaseResp, err := cli.Grant(ctx, int64(cfg.LeaseTTLSecond))
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("grant etcd lease: %w", err)
	}

	prefix := path.Clean(strings.TrimSuffix(cfg.WorkerPrefix, "/"))
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	servicePrefix := prefix + "/" + cfg.ServiceName

	workerID, workerKey, err := claimWorkerID(ctx, cli, servicePrefix, cfg.InstanceID, leaseResp.ID)
	if err != nil {
		_, _ = cli.Revoke(context.Background(), leaseResp.ID)
		_ = cli.Close()
		return nil, err
	}

	sf, err := NewSnowflake(workerID, cfg.EpochMS)
	if err != nil {
		_, _ = cli.Revoke(context.Background(), leaseResp.ID)
		_ = cli.Close()
		return nil, err
	}

	kaCtx, cancel := context.WithCancel(context.Background())
	kaCh, err := cli.KeepAlive(kaCtx, leaseResp.ID)
	if err != nil {
		cancel()
		_, _ = cli.Revoke(context.Background(), leaseResp.ID)
		_ = cli.Close()
		return nil, fmt.Errorf("start keepalive: %w", err)
	}

	g := &ManagedGenerator{
		sf:       sf,
		client:   cli,
		leaseID:  leaseResp.ID,
		workerID: workerID,
		key:      workerKey,
		value:    cfg.InstanceID,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	g.alive.Store(true)

	go func() {
		defer close(g.done)
		for {
			select {
			case <-kaCtx.Done():
				g.alive.Store(false)
				return
			case _, ok := <-kaCh:
				if !ok {
					g.alive.Store(false)
					return
				}
			}
		}
	}()

	return g, nil
}

func claimWorkerID(ctx context.Context, cli *clientv3.Client, servicePrefix, instanceID string, leaseID clientv3.LeaseID) (int64, string, error) {
	for wid := int64(0); wid <= maxWorkerID; wid++ {
		key := fmt.Sprintf("%s/%04d", servicePrefix, wid)
		resp, err := cli.Txn(ctx).
			If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
			Then(clientv3.OpPut(key, instanceID, clientv3.WithLease(leaseID))).
			Commit()
		if err != nil {
			return 0, "", fmt.Errorf("claim worker_id=%d: %w", wid, err)
		}
		if resp.Succeeded {
			return wid, key, nil
		}
	}
	return 0, "", fmt.Errorf("no available worker_id under %s", servicePrefix)
}

func (g *ManagedGenerator) NextID() (int64, error) {
	if !g.alive.Load() {
		return 0, errors.New("worker lease lost; id generation is disabled")
	}
	return g.sf.NextID()
}

func (g *ManagedGenerator) WorkerID() int64 {
	return g.workerID
}

func (g *ManagedGenerator) LeaseAlive() bool {
	return g.alive.Load()
}

func (g *ManagedGenerator) WorkerKey() string {
	return g.key
}

func (g *ManagedGenerator) Close(ctx context.Context) error {
	if g == nil {
		return nil
	}

	g.cancel()
	select {
	case <-g.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	_, _ = g.client.Revoke(ctx, g.leaseID)
	return g.client.Close()
}
