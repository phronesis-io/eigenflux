package tradebff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type CommissionClient struct {
	baseURL *url.URL
	http    *http.Client
}

type commissionEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type UpstreamError struct {
	Status int
	Code   int
	Msg    string
}

func (e *UpstreamError) Error() string { return "Commission request failed" }

func NewCommissionClient(endpoint string) (*CommissionClient, error) {
	baseURL, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || baseURL.Host == "" || (baseURL.Scheme != "https" && !(baseURL.Scheme == "http" && localHost(baseURL.Hostname()))) {
		return nil, fmt.Errorf("invalid Commission endpoint")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	return &CommissionClient{baseURL: baseURL, http: &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *CommissionClient) Do(ctx context.Context, token, method, path string, query url.Values, body []byte, idempotencyKey string) (json.RawMessage, error) {
	if c == nil || token == "" || !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("invalid Commission request")
	}
	target := *c.baseURL
	target.Path = strings.TrimRight(c.baseURL.Path, "/") + path
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Commission request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if key := strings.TrimSpace(idempotencyKey); key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Commission: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read Commission response: %w", err)
	}
	var envelope commissionEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("decode Commission response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Code != 0 {
		status := response.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		return nil, &UpstreamError{Status: status, Code: envelope.Code, Msg: envelope.Msg}
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return json.RawMessage(`{}`), nil
	}
	return envelope.Data, nil
}

func localHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func upstreamStatus(err error) int {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		switch upstream.Status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusTooManyRequests:
			return upstream.Status
		}
	}
	return http.StatusServiceUnavailable
}
