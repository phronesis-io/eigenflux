package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/client"
)

type orderReadTestTransport func(*http.Request) (*http.Response, error)

func (f orderReadTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOrderReadEOFRecovery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		errs      []error
		wantCalls int
		wantErr   error
	}{
		{"success", nil, 1, nil},
		{"EOF recovers", []error{fmt.Errorf("connection: %w", io.EOF)}, 2, nil},
		{"unexpected EOF recovers", []error{io.ErrUnexpectedEOF, io.EOF}, 3, nil},
		{"bounded", []error{io.EOF, io.EOF, io.EOF}, 3, io.EOF},
		{"permanent", []error{io.ErrClosedPipe}, 1, io.ErrClosedPipe},
		{"cancel error", []error{context.Canceled}, 1, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := &client.Client{BaseURL: "https://example.invalid", HTTPClient: &http.Client{Transport: orderReadTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.Path != "/orders/42" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
				calls++
				if calls <= len(tc.errs) {
					return nil, tc.errs[calls-1]
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"order_id":"42"}}`))}, nil
			})}}
			resp, err := readOrderWithEOFRecovery(context.Background(), c, "/orders/42")
			if calls != tc.wantCalls || !errors.Is(err, tc.wantErr) {
				t.Fatalf("calls=%d err=%v; want calls=%d err=%v", calls, err, tc.wantCalls, tc.wantErr)
			}
			if err == nil && (resp == nil || resp.Code != 0) {
				t.Fatalf("unexpected response: %#v", resp)
			}
		})
	}
}

func TestOrderReadDoesNotRetryHTTPOrInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{401, `{"msg":"unauthorized"}`},
		{409, `{"msg":"conflict"}`},
		{500, `{"msg":"server error"}`},
		{200, `{"code":`},
	} {
		calls := 0
		c := &client.Client{BaseURL: "https://example.invalid", HTTPClient: &http.Client{Transport: orderReadTestTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})}}
		_, err := readOrderWithEOFRecovery(context.Background(), c, "/orders/42")
		if err == nil || calls != 1 {
			t.Fatalf("status=%d calls=%d err=%v", tc.status, calls, err)
		}
	}
}

func TestOrderReadCancellation(t *testing.T) {
	for _, phase := range []string{"before", "request", "backoff"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "before" {
				cancel()
			}
			calls := 0
			c := &client.Client{BaseURL: "https://example.invalid", HTTPClient: &http.Client{Transport: orderReadTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if phase == "request" {
					cancel()
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				time.AfterFunc(10*time.Millisecond, cancel)
				return nil, io.EOF
			})}}
			_, err := readOrderWithEOFRecovery(ctx, c, "/orders/42")
			if !errors.Is(err, context.Canceled) || calls > 1 || (phase == "before" && calls != 0) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestOrderReadDoesNotRetryCredentialRefreshEOF(t *testing.T) {
	calls, refreshes := 0, 0
	c := &client.Client{
		BaseURL:        "https://example.invalid",
		OnUnauthorized: func() (string, error) { refreshes++; return "", io.EOF },
		HTTPClient: &http.Client{Transport: orderReadTestTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"msg":"expired"}`))}, nil
		})},
	}
	_, err := readOrderWithEOFRecovery(context.Background(), c, "/orders/42")
	if !errors.Is(err, io.EOF) || calls != 1 || refreshes != 1 {
		t.Fatalf("calls=%d refreshes=%d err=%v", calls, refreshes, err)
	}
}

func TestOrderReadNilCommandContext(t *testing.T) {
	c := &client.Client{BaseURL: "https://example.invalid", HTTPClient: &http.Client{Transport: orderReadTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Context() == nil {
			t.Fatal("request context is nil")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0}`))}, nil
	})}}
	if _, err := readOrderWithEOFRecovery(nil, c, "/orders/42"); err != nil {
		t.Fatal(err)
	}
}

func TestOrderReadPreservesRequestTimeout(t *testing.T) {
	calls := 0
	c := &client.Client{BaseURL: "https://example.invalid", HTTPClient: &http.Client{
		Timeout: 20 * time.Millisecond,
		Transport: orderReadTestTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if _, ok := r.Context().Deadline(); !ok {
				t.Fatal("request lost HTTP timeout")
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}),
	}}
	_, err := readOrderWithEOFRecovery(context.Background(), c, "/orders/42")
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
