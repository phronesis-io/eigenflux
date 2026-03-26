package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
)

var BaseURL = resolveAPIBaseURL()

func resolveAPIBaseURL() string {
	cfg := config.Load()
	return fmt.Sprintf("http://localhost:%d", cfg.ApiPort)
}

func WaitForAPI(t *testing.T) {
	t.Helper()
	lastErr := ""
	for i := 0; i < 60; i++ {
		resp, err := http.Get(BaseURL + "/skill.md")
		if err != nil {
			lastErr = err.Error()
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if resp.StatusCode != 200 {
			lastErr = fmt.Sprintf("skill.md status=%d", resp.StatusCode)
			resp.Body.Close()
			time.Sleep(500 * time.Millisecond)
			continue
		}

		resp.Body.Close()

		// Also verify Auth RPC is discoverable from API gateway to avoid flaky startup race.
		loginProbeBody, _ := json.Marshal(map[string]string{
			"login_method": "probe_invalid_method",
			"email":        "probe@test.com",
		})
		loginResp, loginErr := http.Post(BaseURL+"/api/v1/auth/login", "application/json", bytes.NewReader(loginProbeBody))
		if loginErr != nil {
			lastErr = loginErr.Error()
			time.Sleep(500 * time.Millisecond)
			continue
		}

		loginPayload, _ := io.ReadAll(loginResp.Body)
		loginResp.Body.Close()

		var loginResult map[string]interface{}
		if err := json.Unmarshal(loginPayload, &loginResult); err != nil {
			lastErr = fmt.Sprintf("invalid auth probe response: %s", string(loginPayload))
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Gateway is fully ready once auth request no longer fails with transport-level service error.
		msg, _ := loginResult["msg"].(string)
		code, _ := loginResult["code"].(float64)
		if int(code) == 500 && msg == "auth service error" {
			lastErr = "auth service error (rpc not ready)"
			time.Sleep(500 * time.Millisecond)
			continue
		}

		return
	}
	if lastErr == "" {
		lastErr = "unknown readiness failure"
	}
	t.Fatalf("API server not ready after 30s: %s", lastErr)
}

func DoPost(t *testing.T, path string, body interface{}, token string) map[string]interface{} {
	t.Helper()
	jsonBody, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", BaseURL+path, bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s failed: %v", path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("failed to parse response from POST %s: %v, body: %s", path, err, string(respBody))
	}
	return result
}

func DoPut(t *testing.T, path string, body interface{}, token string) map[string]interface{} {
	t.Helper()
	jsonBody, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT", BaseURL+path, bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s failed: %v", path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("failed to parse response from PUT %s: %v, body: %s", path, err, string(respBody))
	}
	return result
}

func DoGet(t *testing.T, path string, token string) map[string]interface{} {
	t.Helper()
	req, _ := http.NewRequest("GET", BaseURL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s failed: %v", path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("failed to parse response from GET %s: %v, body: %s", path, err, string(respBody))
	}
	return result
}

func DoGetWithHeaders(t *testing.T, path string, token string, headers map[string]string) map[string]interface{} {
	t.Helper()
	req, _ := http.NewRequest("GET", BaseURL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s failed: %v", path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("failed to parse response from GET %s: %v, body: %s", path, err, string(respBody))
	}
	return result
}
