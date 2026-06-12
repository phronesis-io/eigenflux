package chief

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"eigenflux_server/pkg/json"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *Client) Health(ctx context.Context) error {
	return c.doGET(ctx, "/health", nil, nil)
}

func (c *Client) GetAccount(ctx context.Context, agentID string) (*Account, error) {
	var acc Account
	if err := c.doGET(ctx, "/ledger/accounts/"+url.PathEscape(agentID), nil, &acc); err != nil {
		return nil, err
	}
	return &acc, nil
}

// ListEntries calls GET /ledger/entries. Pass empty string for agentID or
// entryType to omit that filter. limit <= 0 omits the limit param.
func (c *Client) ListEntries(ctx context.Context, agentID, entryType string, limit int) ([]Entry, error) {
	q := url.Values{}
	if agentID != "" {
		q.Set("agentId", agentID)
	}
	if entryType != "" {
		q.Set("type", entryType)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var env entriesEnvelope
	if err := c.doGET(ctx, "/ledger/entries", q, &env); err != nil {
		return nil, err
	}
	return env.Entries, nil
}

func (c *Client) doGET(ctx context.Context, path string, q url.Values, out interface{}) error {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("chief GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		ce := &ChiefError{StatusCode: resp.StatusCode, Detail: string(raw)}
		_ = json.Unmarshal(raw, ce)
		return ce
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("unmarshal: %w", err)
		}
	}
	return nil
}
