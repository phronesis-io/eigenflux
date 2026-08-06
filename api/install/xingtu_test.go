package install

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeXingtuClickID(t *testing.T) {
	if got := normalizeXingtuClickID("  xt-click-123  "); got != "xt-click-123" {
		t.Fatalf("trimmed clickid = %q", got)
	}
	if got := normalizeXingtuClickID("bad\nclick"); got != "" {
		t.Fatalf("control-character clickid must be rejected, got %q", got)
	}
	if got := normalizeXingtuClickID(strings.Repeat("x", xingtuClickIDMaxLength+1)); got != "" {
		t.Fatal("oversized clickid must be rejected")
	}
}

func TestReportXingtuConversionUsesLandingClickID(t *testing.T) {
	var callback, eventType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callback = r.URL.Query().Get("callback")
		eventType = r.URL.Query().Get("event_type")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "msg": "success"})
	}))
	defer srv.Close()

	oldClient := xingtuHTTP
	xingtuHTTP = srv.Client()
	defer func() { xingtuHTTP = oldClient }()

	// Redirect callback requests to the mock while preserving the production
	// function's query construction.
	xingtuHTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultTransport.RoundTrip(req)
	})

	code, err := reportXingtuConversion("real-landing-clickid", "0")
	if err != nil || code != 0 {
		t.Fatalf("reportXingtuConversion code=%d err=%v", code, err)
	}
	if callback != "real-landing-clickid" || eventType != "0" {
		t.Fatalf("callback=%q event_type=%q", callback, eventType)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestXingtuCallbackCols(t *testing.T) {
	if code, sent := xingtuCallbackCols("0"); code != "xingtu_cb_activate_code" || sent != "xingtu_cb_activate_sent_at" {
		t.Fatalf("activation columns = %s, %s", code, sent)
	}
	if code, sent := xingtuCallbackCols("1"); code != "xingtu_cb_register_code" || sent != "xingtu_cb_register_sent_at" {
		t.Fatalf("registration columns = %s, %s", code, sent)
	}
}
