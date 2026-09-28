package featureindex

import "context"

// Materializer is the type-independent lifecycle contract used by the scheduler.
// LoadPage completes its writes before advancing; zero marks a completed scan.
type Materializer interface {
	View() string
	Generation() string
	LoadPage(context.Context, int64, int) (int64, error)
}

// Index is the common typed forward-index contract. Implementations preserve
// their source-specific miss policy and version semantics. Reads never fetch ES.
// Write persists an already-built source snapshot without calling a model.
type Index[T any] interface {
	Materializer
	Read(context.Context, []int64) (map[int64]T, error)
	Write(context.Context, T) error
	WriteBatch(context.Context, []T) error
}

var (
	_ Index[AgentDocument]      = AgentIndex{}
	_ Index[BroadcastDocument]  = BroadcastIndex{}
	_ Index[CommissionDocument] = CommissionIndex{}
)
