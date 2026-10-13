package discoverye2e

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// Application processes bind wildcard RPC/HTTP and derived metrics addresses.
// Holding both exact socket ranges prevents fixture stubs and ephemeral client
// connections from taking an apparently free service port during setup.
type servicePortLease struct {
	port      int
	listeners []net.Listener
	once      sync.Once
	err       error
}

func listenReservedPort(address string) ([]net.Listener, int, error) {
	wildcard, err := net.Listen("tcp", address)
	if err != nil {
		return nil, 0, err
	}
	listeners := []net.Listener{wildcard}
	port := wildcard.Addr().(*net.TCPAddr).Port
	// Darwin permits more-specific loopback listeners to share a wildcard
	// address. Linux's wildcard listener already excludes them. Hold the exact
	// addresses used by local fixtures as well, rather than trusting :0 alone.
	if runtime.GOOS == "darwin" {
		for _, host := range []string{"127.0.0.1", "::1"} {
			listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
			if err != nil {
				for _, held := range listeners {
					_ = held.Close()
				}
				return nil, 0, err
			}
			listeners = append(listeners, listener)
		}
	}
	return listeners, port, nil
}

func reserveServicePort() (*servicePortLease, error) {
	var lastErr error
	for attempt := 0; attempt < 100; attempt++ {
		// :0 can allocate above 64535 on Darwin, leaving no valid metrics
		// port. Pick a bounded dynamic candidate instead; the held listeners,
		// rather than a free-port probe, establish ownership. This range also
		// stays below the default Linux and Darwin client ephemeral ranges.
		number, err := rand.Int(rand.Reader, big.NewInt(20000))
		if err != nil {
			return nil, err
		}
		primary, port, err := listenReservedPort(":" + strconv.Itoa(10000+int(number.Int64())))
		if err != nil {
			if !errors.Is(err, syscall.EADDRINUSE) {
				return nil, err
			}
			lastErr = err
			continue
		}
		if port > 65535-1000 {
			for _, listener := range primary {
				_ = listener.Close()
			}
			lastErr = fmt.Errorf("service port %d has no valid derived metrics port", port)
			continue
		}
		metrics, _, err := listenReservedPort(":" + strconv.Itoa(port+1000))
		if err != nil {
			for _, listener := range primary {
				_ = listener.Close()
			}
			if !errors.Is(err, syscall.EADDRINUSE) {
				return nil, err
			}
			lastErr = err
			continue
		}
		return &servicePortLease{port: port, listeners: append(primary, metrics...)}, nil
	}
	return nil, fmt.Errorf("reserve service and metrics ports after 100 candidates: %w", lastErr)
}

func (l *servicePortLease) release() error {
	l.once.Do(func() {
		for _, listener := range l.listeners {
			l.err = errors.Join(l.err, listener.Close())
		}
	})
	return l.err
}

func TestDiscoveryServicePortLease(t *testing.T) {
	leases := make([]*servicePortLease, 0, 4)
	used := map[int]bool{}
	for i := 0; i < 4; i++ {
		lease, err := reserveServicePort()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, lease.release()) })
		leases = append(leases, lease)
		for _, port := range []int{lease.port, lease.port + 1000} {
			require.Positive(t, port)
			require.LessOrEqual(t, port, 65535)
			require.False(t, used[port], "service and metrics leases must not overlap")
			used[port] = true
			for _, host := range []string{"", "127.0.0.1", "::1"} {
				listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
				if listener != nil {
					_ = listener.Close()
				}
				require.Error(t, err, "held %s:%d must not be reusable by fixture or child listeners", host, port)
			}
		}
	}
	for _, lease := range leases {
		require.NoError(t, lease.release())
		require.NoError(t, lease.release(), "explicit handoff and cleanup must be idempotent")
		for _, listener := range lease.listeners {
			// Another process may claim an unowned port after release. Check
			// our descriptors, not the momentary global port availability.
			// Close also cleans up a leaked descriptor before failing.
			require.ErrorIs(t, listener.Close(), net.ErrClosed, "release must close every held listener")
		}
	}
}
