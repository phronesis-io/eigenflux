package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// EigenFluxPort is the exact port defined in
// docs/architecture/fluxspace-contracts.md §7. It is the only surface the
// FluxSpace Hub may use to reach EigenFlux: identity, friend relations, PM and
// Agent Card projections. A running match never calls this port (ADR-0001
// decision 2).
type EigenFluxPort interface {
	EnsureFriend(ctx context.Context, fromAgentID, toAgentID string) error
	SendPM(ctx context.Context, fromAgentID, toAgentID string, body json.RawMessage) (messageID string, err error)
	ListPM(ctx context.Context, agentID string, sinceID string) ([]RawMessage, error)
	RefreshAccessToken(ctx context.Context, agentID string) (token string, err error)
	PublishAgentCardFields(ctx context.Context, agentID string, fields map[string]string) error
}

// RawMessage is one PM as returned by GET /api/v1/pm/fetch. Fetching marks
// messages as read upstream, so callers MUST persist the returned slice before
// doing anything else with it (GDD "Integration cautions" item 3).
type RawMessage struct {
	MsgID      string          `json:"msg_id"`
	ConvID     string          `json:"conv_id"`
	SenderID   string          `json:"sender_id"`
	ReceiverID string          `json:"receiver_id"`
	Content    json.RawMessage `json:"content"`
	IsRead     bool            `json:"is_read"`
	CreatedAt  int64           `json:"created_at"`
}

// UnmarshalJSON unwraps the PM content string. idl/api.thrift declares
// `content` as a required string, and FluxSpace puts a JSON document inside
// that string; without this the caller would see a quoted JSON string instead
// of an object and could not decode an invitation from it.
func (m *RawMessage) UnmarshalJSON(data []byte) error {
	type wire struct {
		MsgID      string          `json:"msg_id"`
		ConvID     string          `json:"conv_id"`
		SenderID   string          `json:"sender_id"`
		ReceiverID string          `json:"receiver_id"`
		Content    json.RawMessage `json:"content"`
		IsRead     bool            `json:"is_read"`
		CreatedAt  int64           `json:"created_at"`
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	content := w.Content
	if len(content) > 0 && content[0] == '"' {
		var inner string
		if err := json.Unmarshal(content, &inner); err != nil {
			return err
		}
		content = json.RawMessage(inner)
	}
	*m = RawMessage{
		MsgID: w.MsgID, ConvID: w.ConvID, SenderID: w.SenderID,
		ReceiverID: w.ReceiverID, Content: content,
		IsRead: w.IsRead, CreatedAt: w.CreatedAt,
	}
	return nil
}

type gatewayEnvelope struct {
	Code int32           `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// TokenRefresher rotates an expired Agent access token. The client injects an
// implementation: the Hub never holds refresh tokens or Agent signing keys
// (ADR-0001 decision 3).
type TokenRefresher interface {
	RefreshAccessToken(ctx context.Context, agentID string) (accessToken string, err error)
}

// RefresherFunc adapts a plain function to TokenRefresher.
type RefresherFunc func(ctx context.Context, agentID string) (string, error)

// RefreshAccessToken implements TokenRefresher.
func (f RefresherFunc) RefreshAccessToken(ctx context.Context, agentID string) (string, error) {
	return f(ctx, agentID)
}

// GatewayPort is a real HTTP client for the EigenFlux gateway. It is not a
// mock: every method issues an HTTP request with the Agent's Bearer token.
// ADR-0001 decision 2 forbids shipping a mock-only build, so tests exercise
// this client against an httptest server; they never replace it in production.
//
// Verified against idl/api.thrift:
//   - POST /api/v1/pm/send            (SendPMReq / SendPMResp)
//   - GET  /api/v1/pm/fetch           (FetchPMReq / FetchPMResp)
//   - POST /api/v1/relations/apply    (SendFriendRequestReq)
//
// Verified against api/agentcard/handlers.go and pkg/agentcard/registry.go
// on 2026-11-04 (closes the profile-fields half of G9):
//   - GET /api/v1/agents/me/card/refresh-context -> data.profile_version
//   - PUT /api/v1/agents/me/profile/fields       -> {expected_version, updates, source}
//
// There is deliberately no default token-refresh URL. idl/api.thrift declares
// no v1 refresh RPC; the real v2 flow is a two-step ed25519 proof
// (POST /api/v2/agent-sessions/refresh-challenges then /refresh in
// api/consolev2/service.go) that requires the Agent's private key and refresh
// token. Inventing /api/v1/auth/refresh produced a request to a 404. A 401 now
// either uses an injected TokenRefresher or fails loudly.
type GatewayPort struct {
	BaseURL     string
	HTTPClient  *http.Client
	Refresher   TokenRefresher
	FieldsPath  string
	ContextPath string

	mu     sync.Mutex
	tokens map[string]string
}

// NewGatewayPort builds a client. tokens maps Agent ID -> current access token;
// it is copied so callers cannot mutate the client's token store.
func NewGatewayPort(baseURL string, tokens map[string]string) *GatewayPort {
	copied := make(map[string]string, len(tokens))
	for k, v := range tokens {
		copied[k] = v
	}
	return &GatewayPort{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		HTTPClient:  &http.Client{},
		ContextPath: "/api/v1/agents/me/card/refresh-context",
		FieldsPath:  "/api/v1/agents/me/profile/fields",
		tokens:      copied,
	}
}

func (g *GatewayPort) token(agentID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tokens[agentID]
}

func (g *GatewayPort) setToken(agentID, token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tokens[agentID] = token
}

func (g *GatewayPort) client() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return http.DefaultClient
}

// EnsureFriend sends a friend request. PM requires an existing friend
// relation (GDD "Integration cautions" item 2), so invitation flows call this
// before the first SendPM to a new contact.
func (g *GatewayPort) EnsureFriend(ctx context.Context, fromAgentID, toAgentID string) error {
	if fromAgentID == "" || toAgentID == "" {
		return fmt.Errorf("EnsureFriend: both agent IDs are required")
	}
	if err := g.do(ctx, fromAgentID, http.MethodPost, "/api/v1/relations/apply",
		map[string]any{"to_uid": toAgentID}, nil); err != nil {
		return fmt.Errorf("EnsureFriend %s -> %s: %w", fromAgentID, toAgentID, err)
	}
	return nil
}

// SendPM carries a structured FluxSpace JSON document as the PM content
// string. idl/api.thrift declares `content` as a required string, so the JSON
// is stringified, not nested. request_id is a stable hash of the body: the
// gateway deduplicates on it for 60 seconds, which makes a retried invitation
// idempotent (docs/dev/pm.md, "request_id").
func (g *GatewayPort) SendPM(ctx context.Context, fromAgentID, toAgentID string, body json.RawMessage) (string, error) {
	if fromAgentID == "" || toAgentID == "" {
		return "", fmt.Errorf("SendPM: both agent IDs are required")
	}
	if len(body) == 0 || !json.Valid(body) {
		return "", fmt.Errorf("SendPM: body must be valid JSON")
	}
	sum := sha256.Sum256(body)
	req := map[string]any{
		"receiver_id": toAgentID,
		"content":     string(body),
		"request_id":  "fluxspace-" + hex.EncodeToString(sum[:8]),
	}
	var data struct {
		MsgID string `json:"msg_id"`
	}
	if err := g.do(ctx, fromAgentID, http.MethodPost, "/api/v1/pm/send", req, &data); err != nil {
		return "", fmt.Errorf("SendPM %s -> %s: %w", fromAgentID, toAgentID, err)
	}
	if data.MsgID == "" {
		return "", fmt.Errorf("SendPM: gateway returned an empty msg_id")
	}
	return data.MsgID, nil
}

// ListPM fetches unread messages from a cursor. The return value must be
// persisted by the caller before any other operation: the upstream fetch marks
// these messages as read.
func (g *GatewayPort) ListPM(ctx context.Context, agentID, sinceID string) ([]RawMessage, error) {
	q := url.Values{}
	if sinceID != "" {
		q.Set("cursor", sinceID)
	}
	path := "/api/v1/pm/fetch"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var data struct {
		Messages   []RawMessage `json:"messages"`
		NextCursor string       `json:"next_cursor"`
	}
	if err := g.do(ctx, agentID, http.MethodGet, path, nil, &data); err != nil {
		return nil, fmt.Errorf("ListPM %s: %w", agentID, err)
	}
	return data.Messages, nil
}

// SetAccessToken lets the credential owner push a freshly rotated token into
// the port. This is the path a real client uses: it owns the refresh token and
// the ed25519 key, so the refresh itself happens on the client side.
func (g *GatewayPort) SetAccessToken(agentID, token string) error {
	if agentID == "" || token == "" {
		return fmt.Errorf("SetAccessToken: agentID and token are required")
	}
	g.setToken(agentID, token)
	return nil
}

// RefreshAccessToken rotates an expired access token through the injected
// TokenRefresher. With no refresher configured it returns an explicit
// configuration error rather than calling a URL that does not exist (G9).
func (g *GatewayPort) RefreshAccessToken(ctx context.Context, agentID string) (string, error) {
	if g.Refresher == nil {
		return "", fmt.Errorf("TOKEN_REFRESH_NOT_CONFIGURED: agent %s has no TokenRefresher; "+
			"the EigenFlux v1 IDL has no refresh RPC and the v2 flow needs the Agent's private key", agentID)
	}
	token, err := g.Refresher.RefreshAccessToken(ctx, agentID)
	if err != nil {
		return "", fmt.Errorf("RefreshAccessToken %s: %w", agentID, err)
	}
	if token == "" {
		return "", fmt.Errorf("RefreshAccessToken %s: refresher returned an empty token", agentID)
	}
	g.setToken(agentID, token)
	return token, nil
}

// editableFieldKinds mirrors pkg/agentcard/registry.go EditableFields in the
// root module. hub/ is an independent Go module and cannot import it, so this
// table must be kept in sync when the gateway field registry changes. It is a
// whitelist, not a display list: any unknown or protected field is rejected
// locally instead of being sent to the gateway for a 400/403.
var editableFieldKinds = map[string]string{
	"agent_name":         "string",
	"agent_description":  "string",
	"human_description":  "string",
	"working_languages":  "string_list",
	"seeking":            "string_list",
	"offering":           "string_list",
	"geo":                "string",
	"timezone":           "string",
	"current_focus":      "string_list",
	"demands":            "string_list",
	"agent_status":       "string_list",
	"human_status":       "string_list",
	"interests_negative": "string_list",
}

// PublishAgentCardFields writes a versioned, field-level Agent Card patch.
// Personality, memories and relationships never enter an Agent Card projection
// (ADR-0001, GDD "Identity mapping"). A 409 optimistic-lock conflict is
// returned to the caller rather than retried: rereading the context is an
// owner decision.
//
// The request shape is verified against api/agentcard/handlers.go:
// GET refresh-context returns data.profile_version, and PUT profile/fields
// expects {expected_version, updates, source}. string_list fields must be JSON
// arrays even when there is only one value.
func (g *GatewayPort) PublishAgentCardFields(ctx context.Context, agentID string, fields map[string]string) error {
	if len(fields) == 0 {
		return fmt.Errorf("PublishAgentCardFields: no fields supplied")
	}
	updates := make(map[string]any, len(fields))
	for name, value := range fields {
		kind, allowed := editableFieldKinds[name]
		if !allowed {
			return fmt.Errorf("PublishAgentCardFields: %q is not an editable Agent Card field", name)
		}
		if value == "" {
			return fmt.Errorf("PublishAgentCardFields: %q has an empty value", name)
		}
		if kind == "string_list" {
			updates[name] = []string{value}
		} else {
			updates[name] = value
		}
	}
	var contextData struct {
		ProfileVersion *int64 `json:"profile_version"`
	}
	contextPath := g.ContextPath
	if contextPath == "" {
		contextPath = "/api/v1/agents/me/card/refresh-context"
	}
	if err := g.do(ctx, agentID, http.MethodGet, contextPath, nil, &contextData); err != nil {
		return fmt.Errorf("PublishAgentCardFields context: %w", err)
	}
	if contextData.ProfileVersion == nil {
		return fmt.Errorf("PublishAgentCardFields: refresh-context returned no profile_version")
	}
	fieldsPath := g.FieldsPath
	if fieldsPath == "" {
		fieldsPath = "/api/v1/agents/me/profile/fields"
	}
	if err := g.do(ctx, agentID, http.MethodPut, fieldsPath, map[string]any{
		"expected_version": *contextData.ProfileVersion,
		"updates":          updates,
		"source":           "fluxspace_hub",
	}, nil); err != nil {
		return fmt.Errorf("PublishAgentCardFields: %w", err)
	}
	return nil
}

// do performs a request with one token-refresh retry on HTTP 401. Access
// tokens expire in 15 minutes (GDD "Integration cautions" item 6).
func (g *GatewayPort) do(ctx context.Context, agentID, method, path string, body any, out any) error {
	token := g.token(agentID)
	status, err := g.request(ctx, token, method, path, body, out)
	if err == nil {
		return nil
	}
	if status != http.StatusUnauthorized {
		return err
	}
	refreshed, refreshErr := g.RefreshAccessToken(ctx, agentID)
	if refreshErr != nil {
		return fmt.Errorf("%w (token refresh also failed: %v)", err, refreshErr)
	}
	_, err = g.request(ctx, refreshed, method, path, body, out)
	return err
}

// request returns the HTTP status alongside the error so do() can distinguish
// an auth failure from a transport or business failure.
func (g *GatewayPort) request(ctx context.Context, token, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.BaseURL+path, reader)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return 0, fmt.Errorf("transport: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("HTTP %d from %s %s: %s",
			resp.StatusCode, method, path, strings.TrimSpace(string(payload)))
	}
	var envelope gatewayEnvelope
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return resp.StatusCode, fmt.Errorf("decode envelope: %w", err)
		}
	}
	if envelope.Code != 0 {
		return resp.StatusCode, fmt.Errorf("gateway code %d: %s", envelope.Code, envelope.Msg)
	}
	if out != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode data: %w", err)
		}
	}
	return resp.StatusCode, nil
}
