package install

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockXHS stands in for adapi.xiaohongshu.com, dispatching on the "method"
// field and recording the aurora.leads body. tokenCalled flips if the
// getAccessToken handshake is performed.
func mockXHS(t *testing.T, leadsBody *map[string]interface{}, tokenCalled *bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		_ = json.Unmarshal(b, &req)
		switch req["method"] {
		case "oauth.getAccessToken":
			*tokenCalled = true
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0, "msg": "成功", "success": true,
				"data": map[string]interface{}{"access_token": "tok_xyz", "access_token_expire_in": 7200},
			})
		case "aurora.leads":
			*leadsBody = req
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "msg": "成功", "success": true})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
}

// TestReportXHSConversionAuthOn: with auth enabled, the module fetches a token
// and attaches it, and the aurora.leads payload carries the required fields.
func TestReportXHSConversionAuthOn(t *testing.T) {
	var leadsBody map[string]interface{}
	var tokenCalled bool
	srv := mockXHS(t, &leadsBody, &tokenCalled)
	defer srv.Close()

	initXHSConfig() // load env config; overridden below for the mock
	oldBase, oldAuth, oldAdv := xhsAPIBase, xhsAuthEnabled, xhsAdvertiserID
	xhsAPIBase, xhsAuthEnabled, xhsAdvertiserID, xhsToken, xhsTokenExp = srv.URL, true, "adv_test", "", 0
	defer func() {
		xhsAPIBase, xhsAuthEnabled, xhsAdvertiserID, xhsToken, xhsTokenExp = oldBase, oldAuth, oldAdv, "", 0
	}()

	code, err := reportXHSConversion("click_abc", EventCopy)
	if err != nil {
		t.Fatalf("reportXHSConversion: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected success code 0, got %d", code)
	}
	if !tokenCalled {
		t.Fatal("getAccessToken should be called when auth enabled")
	}
	if leadsBody == nil {
		t.Fatal("aurora.leads not called")
	}
	if leadsBody["method"] != "aurora.leads" || leadsBody["click_id"] != "click_abc" {
		t.Fatalf("bad aurora.leads body: %v", leadsBody)
	}
	if leadsBody["advertiser_id"] != "adv_test" || leadsBody["event_type"] != EventCopy {
		t.Fatalf("advertiser_id/event_type missing: %v", leadsBody)
	}
	if leadsBody["access_token"] != "tok_xyz" {
		t.Fatalf("access_token not attached when auth on: %v", leadsBody["access_token"])
	}
	if _, ok := leadsBody["conv_time"]; !ok {
		t.Fatal("conv_time missing")
	}
}

// TestReportXHSConversionAuthOff: the bool switch skips the handshake entirely
// and omits access_token from the callback.
func TestReportXHSConversionAuthOff(t *testing.T) {
	var leadsBody map[string]interface{}
	var tokenCalled bool
	srv := mockXHS(t, &leadsBody, &tokenCalled)
	defer srv.Close()

	initXHSConfig() // load env config; overridden below for the mock
	oldBase, oldAuth, oldAdv := xhsAPIBase, xhsAuthEnabled, xhsAdvertiserID
	xhsAPIBase, xhsAuthEnabled, xhsAdvertiserID, xhsToken, xhsTokenExp = srv.URL, false, "adv_test", "", 0
	defer func() {
		xhsAPIBase, xhsAuthEnabled, xhsAdvertiserID, xhsToken, xhsTokenExp = oldBase, oldAuth, oldAdv, "", 0
	}()

	if _, err := reportXHSConversion("click_def", EventInstall); err != nil {
		t.Fatalf("reportXHSConversion: %v", err)
	}
	if tokenCalled {
		t.Fatal("getAccessToken must NOT be called when auth disabled")
	}
	if leadsBody == nil {
		t.Fatal("aurora.leads not called")
	}
	if _, ok := leadsBody["access_token"]; ok {
		t.Fatalf("access_token must be omitted when auth disabled: %v", leadsBody["access_token"])
	}
}

// TestCallbackCols: the copy (101) and install (102) callbacks map to separate
// state columns so they are claimed and retried independently.
func TestCallbackCols(t *testing.T) {
	if c, s := callbackCols(EventCopy); c != "cb101_code" || s != "cb101_sent_at" {
		t.Fatalf("101 cols wrong: %s %s", c, s)
	}
	if c, s := callbackCols(EventInstall); c != "cb102_code" || s != "cb102_sent_at" {
		t.Fatalf("102 cols wrong: %s %s", c, s)
	}
	if c, _ := callbackCols("999"); c != "cb101_code" {
		t.Fatalf("unknown event should default to cb101, got %s", c)
	}
}

func TestXInstallCallbackClaimable(t *testing.T) {
	now := int64(10 * 60 * 1000)
	tests := []struct {
		name   string
		sentAt int64
		want   bool
	}{
		{name: "never claimed", sentAt: 0, want: true},
		{name: "active lease", sentAt: now - xInstallCallbackLease.Milliseconds() + 1, want: false},
		{name: "lease boundary", sentAt: now - xInstallCallbackLease.Milliseconds(), want: false},
		{name: "expired lease", sentAt: now - xInstallCallbackLease.Milliseconds() - 1, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := xInstallCallbackClaimable(tt.sentAt, now); got != tt.want {
				t.Fatalf("xInstallCallbackClaimable(%d, %d) = %v, want %v", tt.sentAt, now, got, tt.want)
			}
		})
	}
}
