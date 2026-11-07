package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"fluxspace.ai/hub/govern"
)

// shutdownTimeout bounds how long a graceful shutdown may wait for in-flight
// requests (notably long-lived SSE streams) to finish. Without a bound a single
// stuck observer could keep the process alive forever after a SIGTERM.
const shutdownTimeout = 5 * time.Second

// closer is the optional interface a governance store may implement to flush
// and release its durable append handle. main does not require the govern
// package to expose Close(): the assertion is done through this small local
// interface, so the binary builds and shuts down cleanly whether or not that
// method has been added yet.
type closer interface {
	Close() error
}

// closeOptional invokes Close when c implements closer, and is a no-op
// otherwise (including for a nil interface). Isolating the optional assertion
// here keeps shutdown testable without a real store and without a compile-time
// dependency on govern's method set at the call site.
func closeOptional(c any) error {
	if c == nil {
		return nil
	}
	if cl, ok := c.(closer); ok {
		return cl.Close()
	}
	return nil
}

// closeGovernanceStore flushes and closes the governance store when a closer is
// available. A typed-nil Store is skipped explicitly; when Store has no Close
// method the optional assertion simply does nothing.
func closeGovernanceStore(components *govern.Components) error {
	if components == nil || components.Store == nil {
		return nil
	}
	if err := closeOptional(components.Store); err != nil {
		return fmt.Errorf("flush governance store: %w", err)
	}
	return nil
}

// Run serves HTTP on ln until ctx is cancelled or Serve fails, then performs a
// bounded graceful shutdown and releases the governance store. It is separate
// from main so its shutdown behaviour can be tested without OS signals or a
// fixed port.
//
// ln is owned by Run: Server.Shutdown closes it (stopping new work immediately)
// and Serve's own cleanup closes it too, so after Run returns the listener is
// released. Real failures are joined and returned, never swallowed; the one
// error deliberately ignored is http.ErrServerClosed, the sentinel Serve returns
// precisely because we asked it to stop.
func Run(ctx context.Context, srv *http.Server, ln net.Listener, components *govern.Components) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	var serveResult error
	served := false
	select {
	case err := <-serveErr:
		// Serve ended on its own before cancellation. Anything other than the
		// clean shutdown sentinel is a real failure and must be surfaced.
		served = true
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveResult = err
		}
	case <-ctx.Done():
		log.Printf("shutdown requested: stopping new work on %s", ln.Addr())
	}

	// Shutdown closes the listener (no new connections accepted), disables
	// keep-alives and drains in-flight requests, bounded by shutdownTimeout so a
	// stuck stream cannot hang the process.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)

	// Wait for Serve to fully release the listener before returning. On the
	// cancellation path Serve has not reported yet; on the early-error path we
	// already consumed its result above.
	if !served {
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveResult = err
		}
	}

	closeErr := closeGovernanceStore(components)

	outcome := errors.Join(serveResult, shutdownErr, closeErr)
	if outcome != nil {
		log.Printf("shutdown finished with errors: %v", outcome)
	} else {
		log.Printf("shutdown complete: listener released, governance store flushed")
	}
	return outcome
}
