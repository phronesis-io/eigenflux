//go:build !darwin && !windows

package dispatch

import (
	"context"
	"net"
)

// Do not select another store, daemon or writer on unsupported platforms.
func dialCodexOwnerIPC(context.Context, Binding) (net.Conn, error) {
	return nil, errCodexOwnerIPCUnsupported
}
