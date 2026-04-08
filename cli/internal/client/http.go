package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type APIResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type APIError struct {
	StatusCode int
	Code       int
	Msg        string
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
	SkillVer   string
	HTTPClient *http.Client
}

func New(baseURL, token, skillVer string) *Client {
	return &Client{
		BaseURL:  baseURL,
		Token:    token,
		SkillVer: skillVer,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) do(method, path string, body interface{}) (*APIResponse, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
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
	if c.SkillVer != "" {
		req.Header.Set("X-Skill-Ver", c.SkillVer)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		var apiResp APIResponse
		json.Unmarshal(respBody, &apiResp)
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Code:       apiResp.Code,
			Msg:        apiResp.Msg,
		}
	}
	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &apiResp, nil
}

func (c *Client) Get(path string, params map[string]string) (*APIResponse, error) {
	if len(params) > 0 {
		v := url.Values{}
		for k, val := range params {
			v.Set(k, val)
		}
		path = path + "?" + v.Encode()
	}
	return c.do("GET", path, nil)
}

func (c *Client) Post(path string, body interface{}) (*APIResponse, error) {
	return c.do("POST", path, body)
}

func (c *Client) Put(path string, body interface{}) (*APIResponse, error) {
	return c.do("PUT", path, body)
}

func (c *Client) Delete(path string) (*APIResponse, error) {
	return c.do("DELETE", path, nil)
}
