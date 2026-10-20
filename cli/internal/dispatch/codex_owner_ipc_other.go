//go:build !darwin

package dispatch

import (
	"context"
	"net"
)

// Windows named-pipe transport is untested; do not guess an endpoint or use a
// different store, daemon or writer when the supported transport is unavailable.
func dialCodexOwnerIPC(context.Context, Binding) (net.Conn, error) {
	return nil, errCodexOwnerIPCUnsupported
}
