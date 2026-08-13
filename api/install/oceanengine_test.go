package install

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeOceanengineClickID(t *testing.T) {
	if got := normalizeOceanengineClickID("  oe-click-123  "); got != "oe-click-123" {
		t.Fatalf("trimmed clickid = %q", got)
	}
	if got := normalizeOceanengineClickID("bad\nclick"); got != "" {
		t.Fatalf("control-character clickid must be rejected, got %q", got)
	}
}

func TestReportOceanengineConversion(t *testing.T) {
	var method, contentType string
	var payload struct {
		EventType string `json:"event_type"`
		Context   struct {
			Ad struct {
				Callback string `json:"callback"`
			} `json:"ad"`
		} `json:"context"`
		Timestamp int64 `json:"timestamp"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, contentType = r.Method, r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "message": "success"})
	}))
	defer srv.Close()

	oldClient := oceanengineHTTP
	oceanengineHTTP = srv.Client()
	defer func() { oceanengineHTTP = oldClient }()
	oceanengineHTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultTransport.RoundTrip(req)
	})

	code, err := reportOceanengineConversion("real-oceanengine-clickid", oceanengineEventActive, 1604888786102)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if method != http.MethodPost || contentType != "application/json" {
		t.Fatalf("method=%q content-type=%q", method, contentType)
	}
	if payload.EventType != "active" || payload.Context.Ad.Callback != "real-oceanengine-clickid" || payload.Timestamp != 1604888786102 {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestOceanengineCallbackCols(t *testing.T) {
	if code, sent := oceanengineCallbackCols(oceanengineEventActive); code != "oceanengine_cb_active_code" || sent != "oceanengine_cb_active_sent_at" {
		t.Fatalf("active columns = %s, %s", code, sent)
	}
	if code, sent := oceanengineCallbackCols(oceanengineEventRegister); code != "oceanengine_cb_register_code" || sent != "oceanengine_cb_register_sent_at" {
		t.Fatalf("register columns = %s, %s", code, sent)
	}
}
