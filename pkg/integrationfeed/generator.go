package integrationfeed

import (
	"context"
	"encoding/json"
)

type Generated struct {
	Payload         json.RawMessage
	ItemCount       int
	MatchingVersion int64
}
type Generator func(ctx context.Context, agentID, matchingVersion int64, limit int) (Generated, error)
