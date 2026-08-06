package install

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReportGoogleAdsInstallConversion(t *testing.T) {
	oldClient := googleAdsHTTP
	oldClientID, oldClientSecret := googleAdsClientID, googleAdsClientSecret
	oldRefreshToken, oldCustomerID := googleAdsRefreshToken, googleAdsCustomerID
	oldLoginCustomerID, oldActionID := googleAdsLoginCustomerID, googleAdsConversionActionID
	t.Cleanup(func() {
		googleAdsHTTP = oldClient
		googleAdsClientID, googleAdsClientSecret = oldClientID, oldClientSecret
		googleAdsRefreshToken, googleAdsCustomerID = oldRefreshToken, oldCustomerID
		googleAdsLoginCustomerID, googleAdsConversionActionID = oldLoginCustomerID, oldActionID
	})

	googleAdsClientID = "client"
	googleAdsClientSecret = "secret"
	googleAdsRefreshToken = "refresh"
	googleAdsCustomerID = "4514335848"
	googleAdsLoginCustomerID = "4293944125"
	googleAdsConversionActionID = "987654321"

	var ingest map[string]interface{}
	googleAdsHTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"access_token":"access"}`
		if req.URL.Host == "datamanager.googleapis.com" {
			if got := req.Header.Get("Authorization"); got != "Bearer access" {
				t.Fatalf("Authorization = %q", got)
			}
			if err := json.NewDecoder(req.Body).Decode(&ingest); err != nil {
				t.Fatalf("decode ingest body: %v", err)
			}
			body = `{"requestId":"request-1"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	code, err := reportGoogleAdsInstallConversion("EF-test", "gclid-1", time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC).UnixMilli())
	if err != nil || code != 0 {
		t.Fatalf("report conversion code=%d err=%v", code, err)
	}
	events, ok := ingest["events"].([]interface{})
	if !ok || len(events) != 1 {
		t.Fatalf("events = %#v", ingest["events"])
	}
	event := events[0].(map[string]interface{})
	if event["transactionId"] != "install:EF-test" || event["eventSource"] != "WEB" {
		t.Fatalf("event = %#v", event)
	}
	ids := event["adIdentifiers"].(map[string]interface{})
	if ids["gclid"] != "gclid-1" {
		t.Fatalf("adIdentifiers = %#v", ids)
	}
}

func TestGoogleAdsOAuthState(t *testing.T) {
	state, err := makeGoogleAdsOAuthState("secret")
	if err != nil {
		t.Fatal(err)
	}
	if !validGoogleAdsOAuthState(state, "secret") {
		t.Fatal("fresh state must validate")
	}
	if validGoogleAdsOAuthState(state, "different-secret") {
		t.Fatal("state signed by another secret must fail")
	}
}
