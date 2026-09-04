package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer at_test" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer at_test")
		}
		if got := r.Header.Get("X-CLI-Ver"); got != "0.0.6" {
			t.Errorf("X-CLI-Ver = %q, want %q", got, "0.0.6")
		}
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("limit param = %q, want %q", r.URL.Query().Get("limit"), "10")
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0, "msg": "success",
			"data": map[string]string{"key": "value"},
		})
	}))
	defer srv.Close()
	c := New(srv.URL, "at_test", "0.0.6", Meta{})
	params := map[string]string{"limit": "10"}
	resp, err := c.Get("/test", params)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if resp.Code != 0 {
		t.Errorf("Code = %d, want 0", resp.Code)
	}
}

func TestClientPost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["email"] != "test@example.com" {
			t.Errorf("email = %q, want test@example.com", body["email"])
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0, "msg": "success",
			"data": map[string]string{"token": "at_abc"},
		})
	}))
	defer srv.Close()
	c := New(srv.URL, "", "0.0.6", Meta{})
	resp, err := c.Post("/auth/login", map[string]string{"email": "test@example.com"})
	if err != nil {
		t.Fatalf("Post error: %v", err)
	}
	if resp.Code != 0 {
		t.Errorf("Code = %d, want 0", resp.Code)
	}
}

func TestClientHandles401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]interface{}{"code": 401, "msg": "unauthorized"})
	}))
	defer srv.Close()
	c := New(srv.URL, "at_expired", "0.0.6", Meta{})
	_, err := c.Get("/test", nil)
	if err == nil {
		t.Error("expected error for 401")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want 401", apiErr.StatusCode)
	}
}

func TestClientRefreshesOnceAndRetriesOriginalRequest(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"intent":"recover"}` {
			t.Errorf("request body = %q", body)
		}
		if requests == 1 {
			if got := r.Header.Get("Authorization"); got != "Bearer stale-token" {
				t.Errorf("first authorization = %q", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"AGENT_AUTH_INVALID","message":"refresh required"}}`))
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer recovered-token" {
			t.Errorf("retry authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"ok":true}}`))
	}))
	defer srv.Close()

	refreshes := 0
	c := New(srv.URL, "stale-token", "0.0.35", Meta{})
	c.OnUnauthorized = func() (string, error) {
		refreshes++
		return "recovered-token", nil
	}
	if _, err := c.Post("/retry", map[string]string{"intent": "recover"}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || refreshes != 1 || c.Token != "recovered-token" {
		t.Fatalf("requests=%d refreshes=%d token=%q", requests, refreshes, c.Token)
	}
}

func TestClientPreservesV2RetryMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "37")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code": "ATTENTION_RATE_LIMITED", "message": "limit reached",
				"details": map[string]interface{}{"retry_after_seconds": 45, "remaining": map[string]int{"total": 0}},
			},
		})
	}))
	defer srv.Close()
	c := New(srv.URL, "at_test", "0.0.34", Meta{})
	_, err := c.Post("/api/v2/agent-attention-items:publish", map[string]interface{}{})
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T (%v)", err, err)
	}
	if apiErr.ErrorCode != "ATTENTION_RATE_LIMITED" || apiErr.RetryAfterSeconds != 37 {
		t.Fatalf("unexpected retry error: %#v", apiErr)
	}
	var details map[string]interface{}
	if json.Unmarshal(apiErr.Details, &details) != nil || details["retry_after_seconds"] != float64(45) {
		t.Fatalf("retry details were not preserved: %s", apiErr.Details)
	}
}

func TestClientFallsBackToV2RetryDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code": "ATTENTION_RATE_LIMITED", "message": "limit reached",
				"details": map[string]interface{}{"retry_after_seconds": 45},
			},
		})
	}))
	defer srv.Close()
	c := New(srv.URL, "at_test", "0.0.34", Meta{})
	_, err := c.Post("/api/v2/agent-attention-items:publish", map[string]interface{}{})
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.RetryAfterSeconds != 45 {
		t.Fatalf("details retry fallback missing: %#v (%v)", apiErr, err)
	}
}

func TestClientPreservesCompatiblePMRateLimitEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "37")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 429,
			"msg":  "Message not sent: do not retry immediately.",
			"error": map[string]interface{}{
				"code": "PM_WAITING_FOR_PEER_REPLY", "message": "Message not sent: do not retry immediately.",
				"details": map[string]interface{}{"conv_id": "456", "limit": 3, "used": 3, "remaining": 0, "retry_after_seconds": 37},
			},
		})
	}))
	defer srv.Close()
	c := New(srv.URL, "at_test", "0.0.34", Meta{})
	_, err := c.Post("/api/v2/pm/messages", map[string]interface{}{})
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T (%v)", err, err)
	}
	if apiErr.StatusCode != 429 || apiErr.Code != 429 || apiErr.ErrorCode != "PM_WAITING_FOR_PEER_REPLY" || apiErr.RetryAfterSeconds != 37 {
		t.Fatalf("unexpected PM rate-limit error: %#v", apiErr)
	}
	if apiErr.Msg != "Message not sent: do not retry immediately." {
		t.Fatalf("message=%q", apiErr.Msg)
	}
	var details map[string]interface{}
	if json.Unmarshal(apiErr.Details, &details) != nil || details["conv_id"] != "456" || details["limit"] != float64(3) {
		t.Fatalf("PM details were not preserved: %s", apiErr.Details)
	}
}

func TestClientDelete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "msg": "success"})
	}))
	defer srv.Close()
	c := New(srv.URL, "at_test", "0.0.6", Meta{})
	resp, err := c.Delete("/items/123")
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if resp.Code != 0 {
		t.Errorf("Code = %d, want 0", resp.Code)
	}
}

func TestClientConditionalGetPreservesStatusAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != `"registry-v1"` {
			t.Errorf("If-None-Match = %q", r.Header.Get("If-None-Match"))
		}
		w.Header().Set("ETag", `"registry-v1"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	c := New(srv.URL, "at_test", "0.0.38", Meta{})
	response, err := c.GetWithHeaders("/registry", nil, map[string]string{"If-None-Match": `"registry-v1"`})
	if err != nil {
		t.Fatal(err)
	}
	if response.HTTPStatus != http.StatusNotModified || response.Header.Get("ETag") != `"registry-v1"` {
		t.Fatalf("response = %#v", response)
	}
}

func TestClientDeleteWithBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["expected_context_revision"] != float64(7) {
			t.Errorf("body = %#v", body)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "msg": "success"})
	}))
	defer srv.Close()
	c := New(srv.URL, "at_test", "0.0.38", Meta{})
	if _, err := c.DeleteWithBody("/intent/1", map[string]interface{}{"expected_context_revision": 7}); err != nil {
		t.Fatal(err)
	}
}
