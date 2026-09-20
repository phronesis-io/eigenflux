package notificationpayload

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

var commissionOrderIDFields = [...]string{
	"event_id",
	"order_id",
	"recipient_agent_id",
	"actor_agent_id",
	"snapshot_id",
}

// NormalizeCommissionOrderIDs preserves Snowflake identifiers as decimal
// strings at JSON client boundaries. Timestamps and versions stay numeric.
func NormalizeCommissionOrderIDs(raw string) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	payload := make(map[string]interface{})
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("commission Order notification payload contains trailing data")
	}
	for _, field := range commissionOrderIDFields {
		value, exists := payload[field]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case json.Number:
			if _, err := strconv.ParseInt(typed.String(), 10, 64); err != nil {
				return nil, errors.New("commission Order notification contains an invalid ID")
			}
			payload[field] = typed.String()
		case string:
			if _, err := strconv.ParseInt(typed, 10, 64); err != nil {
				return nil, errors.New("commission Order notification contains an invalid ID")
			}
		default:
			return nil, errors.New("commission Order notification contains an invalid ID")
		}
	}
	return json.Marshal(payload)
}
