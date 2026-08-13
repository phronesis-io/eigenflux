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

func TestOceanengineEventTimestamp(t *testing.T) {
	tok := &Token{CopiedAt: 1604888786102, ReportedAt: 1604888888000}
	if got := oceanengineEventTimestamp(tok, oceanengineEventForm); got != tok.CopiedAt {
		t.Fatalf("form timestamp = %d, want %d", got, tok.CopiedAt)
	}
	if got := oceanengineEventTimestamp(tok, oceanengineEventCustomerEffective); got != tok.ReportedAt {
		t.Fatalf("customer_effective timestamp = %d, want %d", got, tok.ReportedAt)
	}
}

func TestReportOceanengineH5Conversion(t *testing.T) {
	var method, contentType, path string
	var payload struct {
		EventType string `json:"event_type"`
		Context   struct {
			Ad struct {
				Callback string `json:"callback"`
			} `json:"ad"`
		} `json:"context"`
		Timestamp int64 `json:"timestamp"`
	}
	withOceanengineServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, contentType, path = r.Method, r.Header.Get("Content-Type"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "message": "success"})
	}, func() {
		code, err := reportOceanengineH5Conversion("real-oceanengine-clickid", oceanengineEventForm, 1604888786102)
		if err != nil || code != 0 {
			t.Fatalf("code=%d err=%v", code, err)
		}
	})
	if method != http.MethodPost || contentType != "application/json" || path != "/api/v2/conversion" {
		t.Fatalf("method=%q content-type=%q path=%q", method, contentType, path)
	}
	if payload.EventType != "form" || payload.Context.Ad.Callback != "real-oceanengine-clickid" || payload.Timestamp != 1604888786102 {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestReportOceanengineOmnichannelConversion(t *testing.T) {
	var path string
	var payload struct {
		EventType string `json:"event_type"`
		BizType   int    `json:"biz_type"`
		Context   struct {
			Ad struct {
				ClickID   string `json:"clickid"`
				AuthToken string `json:"auth_token"`
			} `json:"ad"`
		} `json:"context"`
		Properties struct {
			AuthToken string `json:"auth_token"`
			EventTime int64  `json:"event_time"`
			ClickID   string `json:"clickid"`
		} `json:"properties"`
		AttributeLabel string `json:"attribute_label"`
		Source         string `json:"source"`
		Timestamp      int64  `json:"timestamp"`
	}
	withOceanengineServer(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "message": "success"})
	}, func() {
		code, err := reportOceanengineOmnichannelConversion("oe-click", oceanengineEventCustomerEffective, 1604888786102, "server-secret")
		if err != nil || code != 0 {
			t.Fatalf("code=%d err=%v", code, err)
		}
	})
	if path != "/api/v1/attribution" || payload.EventType != "customer_effective" || payload.BizType != 23 {
		t.Fatalf("path=%q payload=%+v", path, payload)
	}
	if payload.Context.Ad.ClickID != "oe-click" || payload.Context.Ad.AuthToken != "server-secret" || payload.Properties.AuthToken != "server-secret" || payload.Properties.ClickID != "oe-click" {
		t.Fatalf("attribution payload=%+v", payload)
	}
	if payload.Timestamp != 1604888786 || payload.Properties.EventTime != 1604888786 || payload.AttributeLabel != "convert" || payload.Source != "oto" {
		t.Fatalf("timestamps and fixed fields=%+v", payload)
	}
}

func TestOceanengineCallbackCols(t *testing.T) {
	cases := []struct {
		destination, eventType, code, sent string
	}{
		{oceanengineDestinationH5, oceanengineEventForm, "oceanengine_h5_form_code", "oceanengine_h5_form_sent_at"},
		{oceanengineDestinationH5, oceanengineEventCustomerEffective, "oceanengine_h5_customer_code", "oceanengine_h5_customer_sent_at"},
		{oceanengineDestinationOmnichannel, oceanengineEventForm, "oceanengine_omni_form_code", "oceanengine_omni_form_sent_at"},
		{oceanengineDestinationOmnichannel, oceanengineEventCustomerEffective, "oceanengine_omni_customer_code", "oceanengine_omni_customer_sent_at"},
	}
	for _, tc := range cases {
		code, sent := oceanengineCallbackCols(tc.destination, tc.eventType)
		if code != tc.code || sent != tc.sent {
			t.Fatalf("%s/%s columns = %s, %s", tc.destination, tc.eventType, code, sent)
		}
	}
}

func withOceanengineServer(t *testing.T, handler http.HandlerFunc, run func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	oldClient := oceanengineHTTP
	oceanengineHTTP = srv.Client()
	defer func() { oceanengineHTTP = oldClient }()
	oceanengineHTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultTransport.RoundTrip(req)
	})
	run()
}
