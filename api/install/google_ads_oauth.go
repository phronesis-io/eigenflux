package install

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

const googleAdsOAuthCallbackPath = "/api/v1/install/google-ads/oauth/callback"

func googleAdsOAuthRedirectURI() string {
	return envStr("GOOGLE_ADS_OAUTH_REDIRECT_URI", "https://www.eigenflux.ai"+googleAdsOAuthCallbackPath)
}

// googleAdsOAuthAuthorize starts a one-time, administrator-protected OAuth flow.
func googleAdsOAuthAuthorize(_ context.Context, c *app.RequestContext) {
	adminToken := envStr("GOOGLE_ADS_OAUTH_ADMIN_TOKEN", "")
	if adminToken == "" || !secureBearerMatch(string(c.GetHeader("Authorization")), adminToken) {
		c.String(http.StatusUnauthorized, "authorization required")
		return
	}
	if googleAdsClientID == "" || googleAdsClientSecret == "" {
		c.String(http.StatusServiceUnavailable, "Google OAuth client is not configured")
		return
	}
	state, err := makeGoogleAdsOAuthState(adminToken)
	if err != nil {
		c.String(http.StatusInternalServerError, "unable to start authorization")
		return
	}
	q := url.Values{
		"client_id":     {googleAdsClientID},
		"redirect_uri":  {googleAdsOAuthRedirectURI()},
		"response_type": {"code"},
		// adwords preserves Google Ads administration access; datamanager is
		// required by the current Google replacement for offline conversions.
		"scope":       {"https://www.googleapis.com/auth/adwords https://www.googleapis.com/auth/datamanager"},
		"access_type": {"offline"},
		"prompt":      {"consent"},
		"state":       {state},
	}
	c.Redirect(http.StatusFound, []byte("https://accounts.google.com/o/oauth2/v2/auth?"+q.Encode()))
}

func googleAdsOAuthCallback(_ context.Context, c *app.RequestContext) {
	adminToken := envStr("GOOGLE_ADS_OAUTH_ADMIN_TOKEN", "")
	if adminToken == "" || !validGoogleAdsOAuthState(c.Query("state"), adminToken) {
		c.String(http.StatusBadRequest, "invalid or expired OAuth state")
		return
	}
	if e := c.Query("error"); e != "" {
		c.String(http.StatusBadRequest, "Google authorization was not granted: "+e)
		return
	}
	code := c.Query("code")
	if code == "" {
		c.String(http.StatusBadRequest, "authorization code is missing")
		return
	}
	form := url.Values{
		"code":          {code},
		"client_id":     {googleAdsClientID},
		"client_secret": {googleAdsClientSecret},
		"redirect_uri":  {googleAdsOAuthRedirectURI()},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequest(http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		c.String(http.StatusInternalServerError, "unable to exchange authorization code")
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := googleAdsHTTP.Do(req)
	if err != nil {
		c.String(http.StatusBadGateway, "Google token exchange failed")
		return
	}
	defer resp.Body.Close()
	var result struct {
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 || result.RefreshToken == "" {
		c.String(http.StatusBadGateway, "Google did not return a refresh token; revoke this app authorization and retry")
		return
	}
	// This value is intentionally not persisted or logged. The authenticated
	// administrator copies it once into the protected server environment.
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<!doctype html><title>Google authorized</title><h1>Google Data Manager authorized</h1><p>Copy this once into the protected server environment as <code>GOOGLE_ADS_REFRESH_TOKEN</code>, then close this page:</p><pre>"+html.EscapeString(result.RefreshToken)+"</pre><p>This value is not stored by the server.</p>"))
}

func secureBearerMatch(header, expected string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return hmac.Equal([]byte(strings.TrimSpace(strings.TrimPrefix(header, prefix))), []byte(expected))
}

func makeGoogleAdsOAuthState(secret string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	payload := fmt.Sprintf("%d.%s", time.Now().Add(10*time.Minute).Unix(), base64.RawURLEncoding.EncodeToString(raw))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func validGoogleAdsOAuthState(state, secret string) bool {
	parts := strings.Split(state, ".")
	if len(parts) != 3 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return false
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	return err == nil && hmac.Equal(got, mac.Sum(nil))
}
