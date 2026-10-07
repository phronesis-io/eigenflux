// Package publishorigin records why an Agent published a broadcast: during an
// automatic heartbeat cycle or because its owner asked. It is telemetry only;
// an absent or unrecognized value is stored as NULL and never rejects a publish.
package publishorigin

import (
	"context"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

const (
	Heartbeat = "heartbeat"
	Owner     = "owner"

	metaKey = "item-publish-origin"
)

// Normalize returns the canonical origin, or "" when raw is absent or not one
// of the recognized values.
func Normalize(raw string) string {
	switch raw {
	case Heartbeat, Owner:
		return raw
	default:
		return ""
	}
}

// WithOrigin carries a recognized origin from the gateway to the item RPC as a
// transient (single-hop) RPC value. Unrecognized values leave ctx unchanged.
func WithOrigin(ctx context.Context, raw string) context.Context {
	origin := Normalize(raw)
	if origin == "" {
		return ctx
	}
	return metainfo.WithValue(ctx, metaKey, origin)
}

// FromContext returns the recognized origin carried by ctx, or "".
func FromContext(ctx context.Context) string {
	value, _ := metainfo.GetValue(ctx, metaKey)
	return Normalize(value)
}
