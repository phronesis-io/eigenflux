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

	initXHSConfig() // load defaults (advertiser_id / event_type); overridden below
	oldBase, oldAuth := xhsAPIBase, xhsAuthEnabled
	xhsAPIBase, xhsAuthEnabled, xhsToken, xhsTokenExp = srv.URL, true, "", 0
	defer func() { xhsAPIBase, xhsAuthEnabled, xhsToken, xhsTokenExp = oldBase, oldAuth, "", 0 }()

	code, err := reportXHSConversion("click_abc")
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
	if leadsBody["advertiser_id"] != xhsAdvertiserID || leadsBody["event_type"] != xhsEventType {
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

	initXHSConfig() // load defaults (advertiser_id / event_type); overridden below
	oldBase, oldAuth := xhsAPIBase, xhsAuthEnabled
	xhsAPIBase, xhsAuthEnabled, xhsToken, xhsTokenExp = srv.URL, false, "", 0
	defer func() { xhsAPIBase, xhsAuthEnabled, xhsToken, xhsTokenExp = oldBase, oldAuth, "", 0 }()

	if _, err := reportXHSConversion("click_def"); err != nil {
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
