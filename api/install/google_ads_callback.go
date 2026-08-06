package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
)

// Google Ads offline conversion upload through the Google Data Manager API.
// A landing gclid is retained with the ref and sent only after installation is
// confirmed. New Google Ads integrations cannot use ConversionUploadService;
// Data Manager is the supported replacement.
var (
	googleAdsEnabled            bool
	googleAdsDeveloperToken     string // Retained for existing configuration compatibility; Data Manager does not use it.
	googleAdsClientID           string
	googleAdsClientSecret       string
	googleAdsRefreshToken       string
	googleAdsCustomerID         string
	googleAdsLoginCustomerID    string
	googleAdsConversionActionID string
	googleAdsAPIVersion         string
)

var googleAdsHTTP = &http.Client{Timeout: 10 * time.Second}

func initGoogleAdsConfig() {
	googleAdsEnabled = envBool("GOOGLE_ADS_ENABLED", false)
	googleAdsDeveloperToken = envStr("GOOGLE_ADS_DEVELOPER_TOKEN", "")
	googleAdsClientID = envStr("GOOGLE_ADS_CLIENT_ID", "")
	googleAdsClientSecret = envStr("GOOGLE_ADS_CLIENT_SECRET", "")
	googleAdsRefreshToken = envStr("GOOGLE_ADS_REFRESH_TOKEN", "")
	googleAdsCustomerID = digitsOnly(envStr("GOOGLE_ADS_CUSTOMER_ID", ""))
	googleAdsLoginCustomerID = digitsOnly(envStr("GOOGLE_ADS_LOGIN_CUSTOMER_ID", ""))
	googleAdsConversionActionID = digitsOnly(envStr("GOOGLE_ADS_CONVERSION_ACTION_ID", ""))
	googleAdsAPIVersion = strings.Trim(envStr("GOOGLE_ADS_API_VERSION", "v22"), "/")
	if googleAdsEnabled && !googleAdsConfigured() {
		logger.Default().Error("GOOGLE_ADS_ENABLED=true but Google Ads Data Manager config is incomplete; callbacks skipped")
	}
}

func googleAdsConfigured() bool {
	return googleAdsClientID != "" && googleAdsClientSecret != "" && googleAdsRefreshToken != "" && googleAdsCustomerID != "" && googleAdsConversionActionID != ""
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fireGoogleAdsInstallCallback(ref string) {
	if !googleAdsEnabled || !googleAdsConfigured() {
		return
	}
	go func() {
		won, tok, err := ClaimGoogleAdsInstallCallback(db.DB, ref)
		if err != nil {
			logger.Default().Error("Google Ads callback claim failed", "ref", ref, "err", err)
			return
		}
		if !won || tok.Gclid == "" {
			return
		}
		code, err := reportGoogleAdsInstallConversion(tok.Token, tok.Gclid, tok.ReportedAt)
		if err != nil {
			logger.Default().Error("Google Ads Data Manager callback failed", "ref", ref, "code", code, "err", err)
		}
		if err := SetGoogleAdsInstallCallbackCode(db.DB, ref, code); err != nil {
			logger.Default().Error("Google Ads callback set code failed", "ref", ref, "err", err)
		}
		if code == 0 {
			event("install_callback_google_ads", ref, "channel", tok.Channel, "conversion_action_id", googleAdsConversionActionID)
		}
	}()
}

func googleAdsAccessToken() (string, error) {
	form := url.Values{
		"client_id":     {googleAdsClientID},
		"client_secret": {googleAdsClientSecret},
		"refresh_token": {googleAdsRefreshToken},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequest(http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := googleAdsHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || body.AccessToken == "" {
		return "", fmt.Errorf("Google OAuth token refresh failed: %s", body.Error)
	}
	return body.AccessToken, nil
}

// reportGoogleAdsInstallConversion sends a single confirmed install through
// Data Manager's events:ingest API. transactionId is stable per ref, so Google
// and our callback lease both receive an idempotency key.
func reportGoogleAdsInstallConversion(ref, gclid string, reportedAt int64) (int, error) {
	accessToken, err := googleAdsAccessToken()
	if err != nil {
		return -2, err
	}
	at := time.Now().UTC()
	if reportedAt > 0 {
		at = time.UnixMilli(reportedAt).UTC()
	}

	destination := map[string]interface{}{
		"reference": "google_ads_install",
		"operatingAccount": map[string]string{
			"accountType": "GOOGLE_ADS",
			"accountId":   googleAdsCustomerID,
		},
		"productDestinationId": googleAdsConversionActionID,
	}
	if googleAdsLoginCustomerID != "" {
		destination["loginAccount"] = map[string]string{
			"accountType": "GOOGLE_ADS",
			"accountId":   googleAdsLoginCustomerID,
		}
	}
	payload := map[string]interface{}{
		"destinations": []map[string]interface{}{destination},
		"events": []map[string]interface{}{{
			"eventTimestamp":        at.Format(time.RFC3339),
			"transactionId":         "install:" + ref,
			"eventSource":           "WEB",
			"destinationReferences": []string{"google_ads_install"},
			"adIdentifiers":         map[string]string{"gclid": gclid},
		}},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return -2, err
	}
	req, err := http.NewRequest(http.MethodPost, "https://datamanager.googleapis.com/v1/events:ingest", bytes.NewReader(data))
	if err != nil {
		return -2, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := googleAdsHTTP.Do(req)
	if err != nil {
		return -2, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("Google Data Manager ingest HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var result struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return -2, err
	}
	if result.RequestID == "" {
		return -2, fmt.Errorf("Google Data Manager response has no requestId")
	}
	logger.Default().Info("Google Ads Data Manager accepted install conversion", "ref", ref, "request_id", result.RequestID)
	return 0, nil
}
