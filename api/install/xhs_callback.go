package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
)

// --- Xiaohongshu 聚光 conversion callback (Loop B) ---
//
// Reports a conversion back to 聚光's optimizer keyed by the original click_id so
// ocpx can optimize bidding. Server-to-server (no landing-page pixel). 聚光's
// attribution window is short (~1 day) while our real install is delayed and
// cross-device, so the callback fires on the earliest in-window signal — the
// /r/<ref> fetch — with the install report as a fallback; ClaimCallback makes it
// exactly-once per ref.
//
// Switches are code defaults, overridable by env for ops without a rebuild:
//
//	XHS_CALLBACK_ENABLED  master switch; keep OFF until 直客 联调 is verified
//	XHS_AUTH_ENABLED      do the getAccessToken handshake (the 3.20 doc says new
//	                      clients may not need it — set false to skip the token)
//	XHS_ADVERTISER_ID / XHS_EVENT_TYPE / XHS_API_BASE
var (
	xhsCallbackEnabled = envBool("XHS_CALLBACK_ENABLED", false)
	xhsAuthEnabled     = envBool("XHS_AUTH_ENABLED", true)
	xhsAdvertiserID    = envStr("XHS_ADVERTISER_ID", "5dfe36e3000000000100b5c3")
	xhsEventType       = envStr("XHS_EVENT_TYPE", "101") // 101 = 表单提交, the generic primary target
	xhsAPIBase         = envStr("XHS_API_BASE", "https://adapi.xiaohongshu.com")
)

const xhsCommonPath = "/api/open/common"

var xhsHTTP = &http.Client{Timeout: 8 * time.Second}

// fireXHSCallback reports the conversion for ref to 聚光 exactly once, in the
// background. Safe to call from multiple triggers (/r/ fetch and install report);
// a no-op unless the master switch is on and ref carries a 聚光 click_id.
func fireXHSCallback(ref string) {
	if !xhsCallbackEnabled {
		return
	}
	go func() {
		won, t, err := ClaimCallback(db.DB, ref)
		if err != nil {
			logger.Default().Error("install xhs callback claim failed", "ref", ref, "err", err)
			return
		}
		if !won || t.ClickID == "" {
			return // already sent, no platform click id, or not a 聚光 click
		}
		if err := reportXHSConversion(t.ClickID); err != nil {
			logger.Default().Error("install xhs callback failed", "ref", ref, "err", err)
			return
		}
		event("install_callback_xhs", ref, "channel", t.Channel, "event_type", xhsEventType)
	}()
}

// reportXHSConversion POSTs one aurora.leads conversion for clickID.
func reportXHSConversion(clickID string) error {
	body := map[string]interface{}{
		"advertiser_id": xhsAdvertiserID,
		"method":        "aurora.leads",
		"event_type":    xhsEventType,
		"conv_time":     time.Now().UnixMilli(),
		"click_id":      clickID,
	}
	if xhsAuthEnabled {
		token, err := getXHSAccessToken()
		if err != nil {
			return fmt.Errorf("get access token: %w", err)
		}
		body["access_token"] = token
	}
	var out xhsResp
	if err := xhsPost(body, &out); err != nil {
		return err
	}
	if out.Code != 0 {
		return fmt.Errorf("aurora.leads code=%d msg=%s", out.Code, out.Msg)
	}
	return nil
}

// --- access token cache (7200s ttl, refreshed a minute early) ---

var (
	xhsTokenMu  sync.Mutex
	xhsToken    string
	xhsTokenExp int64 // unix ms
)

func getXHSAccessToken() (string, error) {
	xhsTokenMu.Lock()
	defer xhsTokenMu.Unlock()
	now := time.Now().UnixMilli()
	if xhsToken != "" && now < xhsTokenExp-60_000 {
		return xhsToken, nil
	}
	var out xhsResp
	if err := xhsPost(map[string]interface{}{
		"advertiser_id": xhsAdvertiserID,
		"method":        "oauth.getAccessToken",
	}, &out); err != nil {
		return "", err
	}
	if out.Code != 0 || out.Data.AccessToken == "" {
		return "", fmt.Errorf("getAccessToken code=%d msg=%s", out.Code, out.Msg)
	}
	ttl := out.Data.ExpireIn
	if ttl <= 0 {
		ttl = 7200
	}
	xhsToken = out.Data.AccessToken
	xhsTokenExp = now + ttl*1000
	return xhsToken, nil
}

type xhsResp struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Success bool   `json:"success"`
	Data    struct {
		AccessToken string `json:"access_token"`
		ExpireIn    int64  `json:"access_token_expire_in"`
	} `json:"data"`
}

func xhsPost(body map[string]interface{}, out *xhsResp) error {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, xhsAPIBase+xhsCommonPath, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := xhsHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		return fmt.Errorf("xhs api 400 (unparseable body)")
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
