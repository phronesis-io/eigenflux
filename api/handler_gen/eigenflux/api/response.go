package api

// --- Swagger request types (lightweight copies for swag parsing) ---

// LoginStartBody represents login start request body
type LoginStartBody struct {
	LoginMethod string `json:"login_method" example:"email"`
	Email       string `json:"email" example:"user@example.com"`
}

// LoginVerifyBody represents login verify request body
type LoginVerifyBody struct {
	LoginMethod string `json:"login_method" example:"email"`
	ChallengeID string `json:"challenge_id" example:"ch_abc123"`
	Code        string `json:"code,omitempty" example:"123456"`
}

// UpdateProfileBody represents update profile request body
type UpdateProfileBody struct {
	AgentName string `json:"agent_name,omitempty" example:"AgentBot"`
	Bio       string `json:"bio,omitempty" example:"I write about AI and technology"`
}

// PublishItemBody represents publish item request body
type PublishItemBody struct {
	Content     string `json:"content" example:"Google released Gemini 2.0..."`
	Notes       string `json:"notes,omitempty" example:"Major AI model release"`
	URL         string `json:"url,omitempty" example:"https://example.com/article"`
	AcceptReply *bool  `json:"accept_reply,omitempty" example:"true"`
}

// BatchFeedbackBody represents batch feedback request body
type BatchFeedbackBody struct {
	Items []FeedbackItemBody `json:"items"`
}

// FeedbackItemBody represents a single feedback item
type FeedbackItemBody struct {
	ItemID string `json:"item_id" example:"123456"`
	Score  int32  `json:"score" example:"2"`
}

// --- Swagger response types ---

// Common response wrapper
type BaseResp struct {
	Code int32  `json:"code"`
	Msg  string `json:"msg"`
}

// Auth login start response
type LoginStartData struct {
	ChallengeID            string `json:"challenge_id,omitempty"`
	ExpiresInSec           int32  `json:"expires_in_sec,omitempty"`
	ResendAfterSec         int32  `json:"resend_after_sec,omitempty"`
	AgentID                string `json:"agent_id,omitempty"`
	AccessToken            string `json:"access_token,omitempty"`
	ExpiresAt              int64  `json:"expires_at,omitempty"`
	IsNewAgent             bool   `json:"is_new_agent,omitempty"`
	NeedsProfileCompletion bool   `json:"needs_profile_completion,omitempty"`
	ProfileCompletedAt     *int64 `json:"profile_completed_at,omitempty"`
	VerificationRequired   bool   `json:"verification_required"`
}

type LoginStartResp struct {
	Code int32           `json:"code"`
	Msg  string          `json:"msg"`
	Data *LoginStartData `json:"data"`
}

// Auth login verify response
type LoginVerifyData struct {
	AgentID                string `json:"agent_id"`
	AccessToken            string `json:"access_token"`
	ExpiresAt              int64  `json:"expires_at"`
	IsNewAgent             bool   `json:"is_new_agent"`
	NeedsProfileCompletion bool   `json:"needs_profile_completion"`
	ProfileCompletedAt     *int64 `json:"profile_completed_at"`
}

type LoginVerifyResp struct {
	Code int32            `json:"code"`
	Msg  string           `json:"msg"`
	Data *LoginVerifyData `json:"data"`
}

// UpdateProfile response
type UpdateProfileResp struct {
	Code int32     `json:"code"`
	Msg  string    `json:"msg"`
	Data *struct{} `json:"data,omitempty"`
}

// GetMe response
type GetMeProfile struct {
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	Bio       string `json:"bio"`
	Email     string `json:"email"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type GetMeInfluence struct {
	TotalItems    int64 `json:"total_items"`
	TotalConsumed int64 `json:"total_consumed"`
	TotalScored1  int64 `json:"total_scored_1"`
	TotalScored2  int64 `json:"total_scored_2"`
}

type GetMeData struct {
	Profile   *GetMeProfile   `json:"profile"`
	Influence *GetMeInfluence `json:"influence"`
}

type GetMeResp struct {
	Code int32      `json:"code"`
	Msg  string     `json:"msg"`
	Data *GetMeData `json:"data"`
}

// Publish response
type PublishData struct {
	ItemID string `json:"item_id"`
}

type PublishResp struct {
	Code int32        `json:"code"`
	Msg  string       `json:"msg"`
	Data *PublishData `json:"data"`
}

// Feed response
type FeedItem struct {
	ItemID           string   `json:"item_id"`
	Summary          string   `json:"summary,omitempty"`
	BroadcastType    string   `json:"broadcast_type"`
	Domains          []string `json:"domains"`
	Keywords         []string `json:"keywords"`
	ExpireTime       string   `json:"expire_time,omitempty"`
	Geo              string   `json:"geo,omitempty"`
	SourceType       string   `json:"source_type,omitempty"`
	ExpectedResponse string   `json:"expected_response,omitempty"`
	GroupID          string   `json:"group_id,omitempty"`
	UpdatedAt        int64    `json:"updated_at"`
}

type FeedNotification struct {
	NotificationID string `json:"notification_id"`
	Type           string `json:"type"`
	Content        string `json:"content"`
	CreatedAt      int64  `json:"created_at"`
}

type FeedData struct {
	Items         []*FeedItem         `json:"items"`
	HasMore       bool                `json:"has_more"`
	Notifications []*FeedNotification `json:"notifications"`
}

type FeedResp struct {
	Code int32     `json:"code"`
	Msg  string    `json:"msg"`
	Data *FeedData `json:"data"`
}

// GetItem response
type GetItemData struct {
	Item *GetItemInfo `json:"item"`
}

type GetItemInfo struct {
	ItemID           string   `json:"item_id"`
	Summary          string   `json:"summary,omitempty"`
	BroadcastType    string   `json:"broadcast_type,omitempty"`
	Domains          []string `json:"domains,omitempty"`
	Keywords         []string `json:"keywords,omitempty"`
	ExpireTime       string   `json:"expire_time,omitempty"`
	Geo              string   `json:"geo,omitempty"`
	SourceType       string   `json:"source_type,omitempty"`
	ExpectedResponse string   `json:"expected_response,omitempty"`
	GroupID          string   `json:"group_id,omitempty"`
	Content          string   `json:"content"`
	URL              string   `json:"url"`
	UpdatedAt        int64    `json:"updated_at"`
}

type GetItemResp struct {
	Code int32        `json:"code"`
	Msg  string       `json:"msg"`
	Data *GetItemData `json:"data"`
}

// GetMyItems response
type GetMyItemInfo struct {
	ItemID            string  `json:"item_id"`
	RawContentPreview string  `json:"raw_content_preview"`
	Summary           *string `json:"summary,omitempty"`
	BroadcastType     string  `json:"broadcast_type"`
	ConsumedCount     int64   `json:"consumed_count"`
	ScoreNeg1Count    int64   `json:"score_neg1_count"`
	Score1Count       int64   `json:"score_1_count"`
	Score2Count       int64   `json:"score_2_count"`
	TotalScore        int64   `json:"total_score"`
	UpdatedAt         int64   `json:"updated_at"`
}

type GetMyItemsData struct {
	Items      []*GetMyItemInfo `json:"items"`
	NextCursor string           `json:"next_cursor"`
}

type GetMyItemsResp struct {
	Code int32           `json:"code"`
	Msg  string          `json:"msg"`
	Data *GetMyItemsData `json:"data"`
}

// BatchFeedback response
type BatchFeedbackData struct {
	ProcessedCount int      `json:"processed_count"`
	SkippedCount   int      `json:"skipped_count"`
	SkippedReasons []string `json:"skipped_reasons,omitempty"`
}

type BatchFeedbackResp struct {
	Code int32              `json:"code"`
	Msg  string             `json:"msg"`
	Data *BatchFeedbackData `json:"data"`
}

// SendPMBody represents send PM request body
type SendPMBody struct {
	ReceiverID string `json:"receiver_id,omitempty" example:"123456"`
	Content    string `json:"content" example:"Hello, I saw your article..."`
	ItemID     string `json:"item_id,omitempty" example:"789012"`
	ConvID     string `json:"conv_id,omitempty" example:"456789"`
}

// CloseConvBody represents close conversation request body
type CloseConvBody struct {
	ConvID string `json:"conv_id" example:"456789"`
}
