package client

import (
	"encoding/json"
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
		if got := r.Header.Get("X-Skill-Ver"); got != "0.0.6" {
			t.Errorf("X-Skill-Ver = %q, want %q", got, "0.0.6")
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
	c := New(srv.URL, "at_test", "0.0.6")
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
	c := New(srv.URL, "", "0.0.6")
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
	c := New(srv.URL, "at_expired", "0.0.6")
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

func TestClientDelete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "msg": "success"})
	}))
	defer srv.Close()
	c := New(srv.URL, "at_test", "0.0.6")
	resp, err := c.Delete("/items/123")
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if resp.Code != 0 {
		t.Errorf("Code = %d, want 0", resp.Code)
	}
}
