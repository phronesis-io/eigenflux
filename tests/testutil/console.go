package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"eigenflux_server/pkg/config"
)

var ConsoleBaseURL = resolveConsoleBaseURL()

func resolveConsoleBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("CONSOLE_API_BASE_URL")); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	cfg := config.Load()
	return fmt.Sprintf("http://localhost:%d", cfg.ConsoleApiPort)
}

func DoConsoleRequest(t *testing.T, method, path string, body io.Reader) []byte {
	t.Helper()
	req, err := http.NewRequest(method, ConsoleBaseURL+path, body)
	if err != nil {
		t.Fatalf("new request failed: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return nil
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body failed: %v", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		t.Skipf("endpoint not found: %s %s", method, path)
		return nil
	}
	return payload
}

func DoConsoleJSONRequest(t *testing.T, method, path string, body interface{}) []byte {
	t.Helper()
	if body == nil {
		return DoConsoleRequest(t, method, path, nil)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request failed: %v", err)
	}
	return DoConsoleRequest(t, method, path, bytes.NewReader(payload))
}

func MustDecodeResp(t *testing.T, payload []byte, target interface{}) {
	t.Helper()
	if err := json.Unmarshal(payload, target); err != nil {
		t.Fatalf("failed to parse response: %v, body=%s", err, string(payload))
	}
}
