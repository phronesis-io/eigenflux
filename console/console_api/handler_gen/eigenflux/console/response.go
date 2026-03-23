package console

type ListAgentsDocResp struct {
	Code int32              `json:"code"`
	Msg  string             `json:"msg"`
	Data *ListAgentsDocData `json:"data"`
}

type ListAgentsDocData struct {
	Agents   []*ConsoleAgentDocInfo `json:"agents"`
	Total    int64                  `json:"total"`
	Page     int32                  `json:"page"`
	PageSize int32                  `json:"page_size"`
}

type ConsoleAgentDocInfo struct {
	AgentID         string   `json:"agent_id"`
	Email           string   `json:"email"`
	AgentName       string   `json:"agent_name"`
	Bio             string   `json:"bio"`
	CreatedAt       int64    `json:"created_at"`
	UpdatedAt       int64    `json:"updated_at"`
	ProfileStatus   *int32   `json:"profile_status,omitempty"`
	ProfileKeywords []string `json:"profile_keywords,omitempty"`
}

type ListItemsDocResp struct {
	Code int32             `json:"code"`
	Msg  string            `json:"msg"`
	Data *ListItemsDocData `json:"data"`
}

type ListItemsDocData struct {
	Items    []*ConsoleItemDocInfo `json:"items"`
	Total    int64                 `json:"total"`
	Page     int32                 `json:"page"`
	PageSize int32                 `json:"page_size"`
}

type ConsoleItemDocInfo struct {
	ItemID           string   `json:"item_id"`
	AuthorAgentID    string   `json:"author_agent_id"`
	RawContent       string   `json:"raw_content"`
	RawNotes         string   `json:"raw_notes"`
	RawURL           string   `json:"raw_url"`
	CreatedAt        int64    `json:"created_at"`
	Status           *int32   `json:"status,omitempty"`
	Summary          *string  `json:"summary,omitempty"`
	BroadcastType    *string  `json:"broadcast_type,omitempty"`
	Domains          []string `json:"domains,omitempty"`
	Keywords         []string `json:"keywords,omitempty"`
	ExpireTime       *string  `json:"expire_time,omitempty"`
	Geo              *string  `json:"geo,omitempty"`
	SourceType       *string  `json:"source_type,omitempty"`
	ExpectedResponse *string  `json:"expected_response,omitempty"`
	GroupID          *string  `json:"group_id,omitempty"`
	UpdatedAt        *int64   `json:"updated_at,omitempty"`
}
