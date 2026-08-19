package commissionapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUploadUsesPresignedGrantWithoutAuthorization(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("Bearer token leaked to object storage")
		}
		if r.Header.Get("X-Upload") != "ok" {
			t.Error("grant header missing")
		}
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Upload(context.Background(), server.Client(), TransferGrant{Method: "PUT", URL: server.URL, Headers: map[string]string{"X-Upload": "ok"}}, path); err != nil {
		t.Fatal(err)
	}
	if body != "payload" {
		t.Fatalf("body = %q", body)
	}
}

func TestDownloadWritesAtomicallyAndHonorsForce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("result")) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "result.txt")
	grant := TransferGrant{Method: "GET", URL: server.URL}
	if err := Download(context.Background(), server.Client(), grant, path, false); err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), server.Client(), grant, path, false); err == nil {
		t.Fatal("expected existing destination error")
	}
	if err := Download(context.Background(), server.Client(), grant, path, true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "result" {
		t.Fatalf("data = %q", data)
	}
}

func TestUploadRejectsExpiredGrantBeforeTransfer(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	err := Upload(context.Background(), server.Client(), TransferGrant{URL: server.URL, ExpiresAt: time.Now().Add(-time.Second).UnixMilli()}, path)
	if err == nil || called {
		t.Fatalf("expired upload err=%v called=%v", err, called)
	}
}
