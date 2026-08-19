package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type APIResponse struct {
	Code       int             `json:"code"`
	Msg        string          `json:"msg"`
	Data       json.RawMessage `json:"data"`
	HTTPStatus int             `json:"-"`
	Header     http.Header     `json:"-"`
}

type APIError struct {
	StatusCode        int
	Code              int
	ErrorCode         string
	Msg               string
	Details           json.RawMessage
	RetryAfterSeconds int64
}

func (e *APIError) Error() string {
	if e.StatusCode == 401 {
		return "authentication required — run 'eigenflux auth login' first"
	}
	return fmt.Sprintf("API error (HTTP %d): %s", e.StatusCode, e.Msg)
}

type Client struct {
	BaseURL    string
	Token      string
	CLIVersion string
	Meta       Meta
	HTTPClient *http.Client
	OnSuccess  func()
	// OnUnauthorized can rotate Agent V2 credentials once and return the new
	// access token. It is intentionally unset for legacy/email clients.
	OnUnauthorized func() (string, error)
}

const maxResponseBytes = 16 << 20

func New(baseURL, token, cliVersion string, meta Meta) *Client {
	return &Client{
		BaseURL:    baseURL,
		Token:      token,
		CLIVersion: cliVersion,
		Meta:       meta,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) do(method, path string, body interface{}) (*APIResponse, error) {
	return c.doWithHeaders(method, path, body, nil)
}

// doWithHeaders is do() with optional per-request headers, applied after the
// standard Meta headers so a caller can attach call-specific metadata
// (e.g. X-Bio-Source on `profile update`).
func (c *Client) doWithHeaders(method, path string, body interface{}, headers map[string]string) (*APIResponse, error) {
	var bodyBytes []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyBytes = data
	}
	for attempt := 0; attempt < 2; attempt++ {
		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequest(method, c.BaseURL+path, bodyReader)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		if c.CLIVersion != "" {
			req.Header.Set("X-CLI-Ver", c.CLIVersion)
		}
		c.Meta.SetHeaders(req.Header)
		for k, v := range headers {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}
		if len(respBody) > maxResponseBytes {
			return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && c.OnUnauthorized != nil {
			newToken, refreshErr := c.OnUnauthorized()
			if refreshErr != nil {
				return nil, refreshErr
			}
			if newToken == "" {
				return nil, fmt.Errorf("credential refresh returned an empty access token")
			}
			c.Token = newToken
			continue
		}
		if resp.StatusCode == http.StatusNotModified {
			if c.OnSuccess != nil {
				c.OnSuccess()
			}
			return &APIResponse{HTTPStatus: resp.StatusCode, Header: resp.Header.Clone()}, nil
		}
		if resp.StatusCode >= 400 {
			var apiResp APIResponse
			_ = json.Unmarshal(respBody, &apiResp)
			var v2Resp struct {
				Error struct {
					Code    string          `json:"code"`
					Message string          `json:"message"`
					Details json.RawMessage `json:"details"`
				} `json:"error"`
			}
			_ = json.Unmarshal(respBody, &v2Resp)
			message := apiResp.Msg
			if message == "" {
				message = v2Resp.Error.Message
			}
			return nil, &APIError{
				StatusCode: resp.StatusCode, Code: apiResp.Code, ErrorCode: v2Resp.Error.Code,
				Msg: message, Details: v2Resp.Error.Details,
				RetryAfterSeconds: parseRetryAfterSeconds(resp.Header.Get("Retry-After"), v2Resp.Error.Details),
			}
		}
		var apiResp APIResponse
		if err := json.Unmarshal(respBody, &apiResp); err != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}
		apiResp.HTTPStatus = resp.StatusCode
		apiResp.Header = resp.Header.Clone()
		if c.OnSuccess != nil {
			c.OnSuccess()
		}
		return &apiResp, nil
	}
	return nil, fmt.Errorf("request retry budget exhausted")
}

func parseRetryAfterSeconds(header string, details json.RawMessage) int64 {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); err == nil && seconds > 0 {
		return seconds
	}
	var payload struct {
		RetryAfterSeconds int64 `json:"retry_after_seconds"`
	}
	if json.Unmarshal(details, &payload) == nil && payload.RetryAfterSeconds > 0 {
		return payload.RetryAfterSeconds
	}
	return 0
}

func (c *Client) Get(path string, params map[string]string) (*APIResponse, error) {
	return c.GetWithHeaders(path, params, nil)
}

func (c *Client) GetWithHeaders(path string, params map[string]string, headers map[string]string) (*APIResponse, error) {
	if len(params) > 0 {
		v := url.Values{}
		for k, val := range params {
			v.Set(k, val)
		}
		path = path + "?" + v.Encode()
	}
	return c.doWithHeaders("GET", path, nil, headers)
}

func (c *Client) Post(path string, body interface{}) (*APIResponse, error) {
	return c.do("POST", path, body)
}

func (c *Client) PostWithHeaders(path string, body interface{}, headers map[string]string) (*APIResponse, error) {
	return c.doWithHeaders("POST", path, body, headers)
}

func (c *Client) Put(path string, body interface{}) (*APIResponse, error) {
	return c.do("PUT", path, body)
}

// PutWithHeaders is Put with optional per-request headers.
func (c *Client) PutWithHeaders(path string, body interface{}, headers map[string]string) (*APIResponse, error) {
	return c.doWithHeaders("PUT", path, body, headers)
}

func (c *Client) Delete(path string) (*APIResponse, error) {
	return c.do("DELETE", path, nil)
}

// DeleteWithBody sends a JSON deletion precondition. Versioned resources use
// it so a delete cannot silently apply to a newer owner-confirmed revision.
func (c *Client) DeleteWithBody(path string, body interface{}) (*APIResponse, error) {
	return c.do("DELETE", path, body)
}
