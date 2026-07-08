package install

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
)

// X Ads Conversion API callback reports install success server-to-server.
// It is keyed by the twclid captured from /installx and deduped by ref.
var (
	xCallbackEnabled   bool
	xAdsAPIBase        string
	xAdsAPIVersion     string
	xAdsAccountID      string
	xPixelID           string
	xInstallEventID    string
	xConsumerKey       string
	xConsumerSecret    string
	xAccessToken       string
	xAccessTokenSecret string
)

var xAdsHTTP = &http.Client{Timeout: 8 * time.Second}

func initXAdsConfig() {
	xCallbackEnabled = envBool("X_CALLBACK_ENABLED", false)
	xAdsAPIBase = strings.TrimRight(envStr("X_ADS_API_BASE", "https://ads-api.x.com"), "/")
	xAdsAPIVersion = strings.Trim(envStr("X_ADS_API_VERSION", "12"), "/")
	xAdsAccountID = envStr("X_ADS_ACCOUNT_ID", "")
	xPixelID = envStr("X_PIXEL_ID", "rcmcb")
	xInstallEventID = envStr("X_INSTALL_EVENT_ID", "tw-rcmcb-rdiwm")
	xConsumerKey = envStr("X_CONSUMER_KEY", "")
	xConsumerSecret = envStr("X_CONSUMER_SECRET", "")
	xAccessToken = envStr("X_ACCESS_TOKEN", "")
	xAccessTokenSecret = envStr("X_ACCESS_TOKEN_SECRET", "")
	if xCallbackEnabled && !xAdsConfigured() {
		logger.Default().Error("X_CALLBACK_ENABLED=true but X Ads Conversion API config is incomplete; callbacks skipped")
	}
}

func xAdsConfigured() bool {
	return xAdsAccountID != "" && xPixelID != "" && xInstallEventID != "" && xConsumerKey != "" && xConsumerSecret != "" && xAccessToken != "" && xAccessTokenSecret != ""
}

func fireXAdsInstallCallback(ref string) {
	if !xCallbackEnabled || !xAdsConfigured() {
		return
	}
	go func() {
		won, t, err := ClaimXInstallCallback(db.DB, ref)
		if err != nil {
			logger.Default().Error("install x ads callback claim failed", "ref", ref, "err", err)
			return
		}
		if !won || t.Twclid == "" {
			return
		}
		code, err := reportXAdsInstallConversion(t.Token, t.Twclid, t.ReportedAt)
		if err != nil {
			logger.Default().Error("install x ads callback failed", "ref", ref, "code", code, "err", err)
		}
		if e := SetXInstallCallbackCode(db.DB, ref, code); e != nil {
			logger.Default().Error("install x ads callback set code failed", "ref", ref, "err", e)
		}
		if code == 0 {
			event("install_callback_x_ads", ref, "channel", t.Channel, "event_id", xInstallEventID)
		}
	}()
}

func reportXAdsInstallConversion(ref, twclid string, reportedAt int64) (int, error) {
	conversionTime := time.Now().UTC()
	if reportedAt > 0 {
		conversionTime = time.UnixMilli(reportedAt).UTC()
	}
	body := map[string]interface{}{
		"conversions": []map[string]interface{}{
			{
				"conversion_time": conversionTime.Format(time.RFC3339Nano),
				"event_id":        xInstallEventID,
				"identifiers": []map[string]string{
					{"twclid": twclid},
				},
				"conversion_id": ref,
				"description":   "EigenFlux install complete",
			},
		},
	}
	buf, _ := json.Marshal(body)
	endpoint := fmt.Sprintf("%s/%s/measurement/conversions/%s", xAdsAPIBase, xAdsAPIVersion, url.PathEscape(xPixelID))
	q := url.Values{}
	q.Set("account_id", xAdsAccountID)
	endpoint += "?" + q.Encode()

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return -2, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := signOAuth1(req); err != nil {
		return -2, err
	}
	resp, err := xAdsHTTP.Do(req)
	if err != nil {
		return -2, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("x ads api status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out struct {
		Data struct {
			ConversionsProcessed int    `json:"conversions_processed"`
			DebugID              string `json:"debug_id"`
		} `json:"data"`
	}
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &out); err != nil {
			return -2, err
		}
	}
	if out.Data.ConversionsProcessed <= 0 {
		return -2, fmt.Errorf("x ads api accepted but processed no conversions body=%s", strings.TrimSpace(string(respBody)))
	}
	return 0, nil
}

func signOAuth1(req *http.Request) error {
	nonce, err := oauthNonce()
	if err != nil {
		return err
	}
	oauthParams := map[string]string{
		"oauth_consumer_key":     xConsumerKey,
		"oauth_nonce":            nonce,
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        strconv.FormatInt(time.Now().Unix(), 10),
		"oauth_token":            xAccessToken,
		"oauth_version":          "1.0",
	}
	params := url.Values{}
	for k, vs := range req.URL.Query() {
		for _, v := range vs {
			params.Add(k, v)
		}
	}
	for k, v := range oauthParams {
		params.Add(k, v)
	}
	baseURL := *req.URL
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	baseParts := []string{
		strings.ToUpper(req.Method),
		oauthEscape(baseURL.String()),
		oauthEscape(normalizeOAuthParams(params)),
	}
	baseString := strings.Join(baseParts, "&")
	signingKey := oauthEscape(xConsumerSecret) + "&" + oauthEscape(xAccessTokenSecret)
	mac := hmac.New(sha1.New, []byte(signingKey))
	mac.Write([]byte(baseString))
	oauthParams["oauth_signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))

	keys := make([]string, 0, len(oauthParams))
	for k := range oauthParams {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=\"%s\"", oauthEscape(k), oauthEscape(oauthParams[k])))
	}
	req.Header.Set("Authorization", "OAuth "+strings.Join(parts, ", "))
	return nil
}

func oauthNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func normalizeOAuthParams(values url.Values) string {
	type pair struct{ k, v string }
	pairs := make([]pair, 0)
	for k, vs := range values {
		for _, v := range vs {
			pairs = append(pairs, pair{oauthEscape(k), oauthEscape(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k == pairs[j].k {
			return pairs[i].v < pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

func oauthEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
