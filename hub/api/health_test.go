package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthReturns200(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHealth(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type = %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ok" || body["service"] != "fluxspace-hub" {
		t.Errorf("unexpected body: %v", body)
	}
}

func TestHealthRejectsOtherMethods(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHealth(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /health = %d, want 405", rec.Code)
	}
}

// A deployment check must be able to answer "is this Hub gated" without a
// credential, or the setting that most needs verifying is the one nobody can see.
func TestHealthReportsAuthMode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tokens map[string]string
		want   string
	}{
		{"unauthenticated", nil, "disabled"},
		{"authenticated", map[string]string{"tok-owner-a": "owner_a"}, "enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			RegisterHealth(mux, &Server{OwnerTokens: tc.tokens})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if body["auth"] != tc.want {
				t.Fatalf("auth = %q, want %q", body["auth"], tc.want)
			}
		})
	}
}
