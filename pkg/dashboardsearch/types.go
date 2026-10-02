// Package dashboardsearch defines the private Dashboard search response shared by its data owners.
package dashboardsearch

type Result struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Preview        string `json:"preview"`
	Status         string `json:"status"`
	URL            string `json:"url"`
	UpdatedAt      int64  `json:"updated_at"`
	ConversationID string `json:"conversation_id,omitempty"`
	PeerID         string `json:"peer_id,omitempty"`
}
type Group struct {
	Type       string   `json:"type"`
	Items      []Result `json:"items"`
	NextCursor string   `json:"next_cursor"`
	HasMore    bool     `json:"has_more"`
	Error      string   `json:"error,omitempty"`
}
